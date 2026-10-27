package testutil

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/backup"
	"taskronin/internal/clock"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
)

// SvcOpt changes a service fixture's options before construction.
type SvcOpt func(*service.Options)

// Service constructs a service with the catalog clock, policies and isolated storage.
func Service(t testing.TB, opts ...SvcOpt) (*service.Service, *clock.Fake) {
	t.Helper()
	svc, clk, _ := newService(t, opts...)
	return svc, clk
}

func newService(t testing.TB, opts ...SvcOpt) (*service.Service, *clock.Fake, *store.Store) {
	t.Helper()
	st, clk := Store(t), Clock(t, DefaultNow)
	ledger, _, err := backup.OpenLedger(filepath.Join(t.TempDir(), "purge-ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	o := service.Options{Store: st, Clock: clk, Zone: Zone, WeekStart: time.Monday, DueSoonDays: 3,
		SlipGrace: 15 * time.Minute, UndoDepth: 1000, Ledger: ledger, Passwords: auth.NewPasswordGate(clk),
		Backups: &backup.Manager{Dir: t.TempDir(), Clock: clk, Snap: st.Snap}, Logger: slog.New(slog.DiscardHandler)}
	for _, opt := range opts {
		opt(&o)
	}
	svc, err := service.New(o)
	if err != nil {
		t.Fatal(err)
	}
	// AIDEV-TODO: Task 17 adds Reconcile + CheckAll cleanup; Task 18's stopping
	// state switches cleanup to CheckStructural as required by ruling F1.
	return svc, clk, o.Store
}

// Owner returns the trusted CLI owner actor.
func Owner() domain.Actor {
	return domain.Actor{Kind: domain.ActorOwner, Scopes: domain.AllScopes, Channel: domain.ChanCLI, RemoteKey: "socket"}
}

// TokenActor returns an API actor with the supplied token name and scopes.
func TokenActor(name string, sc domain.Scopes) domain.Actor {
	return domain.Actor{Kind: domain.ActorToken, TokenName: name, Scopes: sc, Channel: domain.ChanAPI}
}

// AI returns the catalog's AI actor with the MCP mutation limit.
func AI() domain.Actor {
	a := TokenActor("ai", domain.MCPDefaultScopes)
	a.Channel, a.MutationsPerHour = domain.ChanMCP, 60
	return a
}

// Meta constructs a unique canonical test request for the given caller and generation.
func Meta(a domain.Actor, gen domain.Generation) service.Meta {
	key := domain.NewOpID().String()
	return service.Meta{Actor: a, Gen: gen, Idem: service.Idem{Key: key, Method: "POST", Route: "test", Body: []byte(key)}}
}

// RequireCode fails unless err is a domain error with the expected code.
func RequireCode(t testing.TB, err error, want domain.Code) {
	t.Helper()
	de, ok := domain.AsError(err)
	if !ok || de.Code != want {
		t.Fatalf("error = %v, want domain code %s", err, want)
	}
}
