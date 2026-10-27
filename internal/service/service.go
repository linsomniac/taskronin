// Package service implements Taskronin's authorization and transactional rules.
package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/backup"
	"taskronin/internal/civil"
	"taskronin/internal/clock"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// Options supplies the service's dependencies and policy settings.
type Options struct {
	Store       *store.Store
	Clock       clock.Clock
	Zone        civil.Zone
	WeekStart   time.Weekday
	DueSoonDays int
	SlipGrace   time.Duration
	UndoDepth   int
	Backups     *backup.Manager
	Ledger      *backup.Ledger
	Passwords   *auth.PasswordGate
	Logger      *slog.Logger
}

// Service coordinates operations on a single database.
type Service struct {
	store       *store.Store
	clock       clock.Clock
	zone        civil.Zone
	weekStart   time.Weekday
	dueSoonDays int
	slipGrace   time.Duration
	undoDepth   int
	backups     *backup.Manager
	backupGate  chan struct{}
	ledger      *backup.Ledger
	passwords   *auth.PasswordGate
	logger      *slog.Logger
	gen         domain.Generation
	idemHMACKey [32]byte
	life        lifecycle
	usage       usage
	tzChanged   atomic.Bool
	backupHook  func(context.Context) error // installed only by export_test.go
}

// New loads the database identity and initializes the service.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Clock == nil || o.Zone.Location() == nil {
		return nil, domain.Errorf(domain.CodeInternal, "service requires a store, clock and zone")
	}
	if o.SlipGrace == 0 {
		o.SlipGrace = 15 * time.Minute
	}
	if o.UndoDepth == 0 {
		o.UndoDepth = 1000
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Passwords == nil {
		o.Passwords = auth.NewPasswordGate(o.Clock)
	}
	tx, err := o.Store.Reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, serviceError(err)
	}
	defer func() { _ = tx.Rollback() }()
	gen, err := store.GetSetting(context.Background(), tx, "db_generation")
	if err != nil {
		return nil, serviceError(err)
	}
	a, err := store.GetAuthState(context.Background(), tx)
	if err != nil {
		return nil, serviceError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, serviceError(err)
	}
	return &Service{store: o.Store, clock: o.Clock, zone: o.Zone, weekStart: o.WeekStart,
		dueSoonDays: o.DueSoonDays, slipGrace: o.SlipGrace, undoDepth: o.UndoDepth,
		backups: o.Backups, backupGate: make(chan struct{}, 1), ledger: o.Ledger, passwords: o.Passwords, logger: o.Logger,
		gen: domain.Generation(gen), idemHMACKey: a.IdemHMACKey,
		usage: usage{tokens: make(map[domain.TokenID]time.Time), sessions: make(map[[32]byte]time.Time)}}, nil
}

// Generation returns the cached database generation without authorization.
func (s *Service) Generation() domain.Generation { return s.gen }

func (s *Service) systemMeta() Meta {
	return Meta{Actor: domain.SystemActor(), Gen: s.Generation()}
}

// Zone returns the service's configured civil time zone.
func (s *Service) Zone() civil.Zone { return s.zone }

// Meta accompanies each mutation with its caller and request identity.
type Meta struct {
	Actor domain.Actor
	Idem  Idem
	Gen   domain.Generation
}

// Idem identifies a canonical mutation request for idempotency.
type Idem struct {
	Key, Method, Route string
	Body               []byte
}

// OpResult describes a committed operation and its current entity representations.
type OpResult struct {
	OpID                        domain.OpID
	Created, Replayed, NoChange bool
	Refs                        []domain.EntityRef
	Task                        *domain.TaskView
	Entry                       *domain.LogEntry
	Block                       *domain.BlockView
	Token                       *domain.Token
	Tasks                       []domain.TaskView
	Effects                     []Effect
	Warnings                    []string
}

// Page is a result page; an empty NextCursor denotes the last page.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

func (s *Service) read(ctx context.Context, a domain.Actor, scopes domain.Scopes, reconcileFirst bool,
	fn func(q store.Q, now time.Time, today civil.Date) error) error {
	if !a.Scopes.Has(scopes) {
		return domain.Errorf(domain.CodeForbidden, "insufficient scopes")
	}
	if reconcileFirst {
		if err := s.Reconcile(ctx); err != nil {
			return err
		}
	}
	tx, err := s.store.Reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return serviceError(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.clock.Now().Truncate(time.Millisecond)
	if err := fn(tx, now, s.zone.DateOf(now)); err != nil {
		return serviceError(err)
	}
	return serviceError(tx.Commit())
}

func (s *Service) writeTx(ctx context.Context, fn func(tx *sql.Tx, now time.Time) error) error {
	leave, err := s.enter()
	if err != nil {
		return err
	}
	defer leave()
	return s.writeTxEntered(ctx, fn)
}

// writeTxEntered runs a writer transaction without changing lifecycle admission.
// Callers hold an in-flight registration, except maintTx for final maintenance
// after Drain.
func (s *Service) writeTxEntered(ctx context.Context, fn func(tx *sql.Tx, now time.Time) error) error {
	tx, err := s.store.Writer.BeginTx(ctx, nil)
	if err != nil {
		return serviceError(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.clock.Now().Truncate(time.Millisecond)
	if err := fn(tx, now); err != nil {
		return serviceError(err)
	}
	return serviceError(tx.Commit())
}

func validation(field string, err error) *domain.Error {
	return domain.ValidationError(domain.FieldError{Field: field, Message: err.Error()})
}

func serviceError(err error) error {
	if err == nil {
		return nil
	}
	err = store.MapErr(err)
	if errors.Is(err, store.ErrNotFound) {
		return &domain.Error{Code: domain.CodeNotFound, Err: err}
	}
	de, _ := domain.AsError(err)
	return de
}
