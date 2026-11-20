package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// IdemTTL is the retention period for an idempotency record.
const IdemTTL = 7 * 24 * time.Hour

func idemHash(key [32]byte, actorKey string, i Idem) [32]byte {
	h := hmac.New(sha256.New, key[:])
	for _, part := range []string{actorKey, i.Method, i.Route} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write(i.Body)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func validateIdemKey(key string) error {
	if key == "" {
		return domain.Errorf(domain.CodeIdemRequired, "idempotency key is required")
	}
	valid := len(key) >= 16 && len(key) <= 128
	for i := 0; valid && i < len(key); i++ {
		c := key[i]
		valid = c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-'
	}
	if !valid {
		return domain.Errorf(domain.CodeMalformed, "idempotency key must match [A-Za-z0-9_-]{16,128}")
	}
	return nil
}

func (s *Service) lookupIdem(ctx context.Context, q store.Q, m Meta, hash [32]byte, now time.Time) (*store.IdemRow, error) {
	row, err := store.GetIdem(ctx, q, m.Actor.Key(), m.Idem.Key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.CreatedAt.Before(now.Add(-IdemTTL)) {
		return nil, nil
	}
	if !hmac.Equal(hash[:], row.Hash[:]) {
		return nil, domain.Errorf(domain.CodeIdemMismatch, "idempotency key was used for a different request")
	}
	return &row, nil
}

// AIDEV-NOTE: Decode the persisted problem's wire field names explicitly;
// domain.RevertConflict has no JSON tags for op_id and entity_id.
type idemProblem struct {
	Code      domain.Code    `json:"code"`
	Detail    string         `json:"detail"`
	Conflicts []idemConflict `json:"conflicts,omitempty"`
}

type idemConflict struct {
	OpID     domain.OpID       `json:"op_id"`
	Kind     domain.OpKind     `json:"kind"`
	Actor    string            `json:"actor"`
	At       time.Time         `json:"at"`
	Entity   domain.EntityKind `json:"entity"`
	EntityID int64             `json:"entity_id"`
}

func (s *Service) replayIdem(ctx context.Context, row store.IdemRow) (*OpResult, error) {
	if row.Status == 409 {
		var problem idemProblem
		if err := json.Unmarshal(row.ErrorJSON, &problem); err != nil {
			return nil, serviceError(err)
		}
		err := &domain.Error{Code: problem.Code, Detail: problem.Detail}
		for _, conflict := range problem.Conflicts {
			err.Conflicts = append(err.Conflicts, domain.RevertConflict(conflict))
		}
		return nil, err
	}
	r := &OpResult{Replayed: true, Created: row.Status == 201, NoChange: !row.OpID.Valid, Refs: row.Refs}
	if row.OpID.Valid {
		r.OpID = row.OpID.V
		err := s.read(ctx, domain.SystemActor(), 0, false, func(q store.Q, _ time.Time, _ civil.Date) error {
			rec, err := store.GetOp(ctx, q, r.OpID)
			if err != nil {
				return err
			}
			events, err := store.EventsOfOp(ctx, q, r.OpID)
			if err != nil {
				return err
			}
			r.Effects, err = persistedEffects(ctx, q, rec, events)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	if err := s.loadResult(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// ExpireIdempotency removes records strictly older than IdemTTL without events.
func (s *Service) ExpireIdempotency(ctx context.Context) (int, error) {
	var n int64
	err := s.writeTx(ctx, func(tx *sql.Tx, now time.Time) error {
		var err error
		n, err = store.DeleteIdemBefore(ctx, tx, now.Add(-IdemTTL))
		return err
	})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}
