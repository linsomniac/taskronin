package service

import (
	"context"
	"encoding/json"
	"maps"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// TestOpRaw runs fixture SQL through the runner and bumps each touched ref once.
func (s *Service) TestOpRaw(ctx context.Context, m Meta, touch []domain.EntityRef, fn func(q store.Q) error) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: "test.raw", class: classForward, scopes: domain.ScopeWrite}, func(o *op) error {
		if err := fn(o.tx); err != nil {
			return err
		}
		for _, ref := range touch {
			o.markDirty(ref)
		}
		return o.record(domain.Event{Entity: domain.EntSetting, Action: domain.ActUpdate, After: json.RawMessage(`{"key":"test.raw","value":"fixture"}`)})
	})
}

// EffectsFromEventsForTest exposes effect reconstruction to external tests.
var EffectsFromEventsForTest = effectsFromEvents

// DuePhaseForTest exposes due classification to external tests.
var DuePhaseForTest = duePhase

// DeriveForTest exposes entry-based block outcome derivation to external tests.
func DeriveForTest(ctx context.Context, q store.Q, task domain.TaskID, d civil.Date) (domain.BlockStatus, error) {
	return derive(ctx, q, task, d)
}

// BlockChangeForTest exposes transitions before the outcome and revert APIs exist.
type BlockChangeForTest = blockChange

// TestSetBlock exercises a block transition through the runner, optionally placing first.
func (s *Service) TestSetBlock(ctx context.Context, m Meta, id domain.BlockID, ch BlockChangeForTest, placed bool) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: "test.block", class: classForward, scopes: domain.ScopeWrite}, func(o *op) error {
		b, err := o.loadBlock(id, nil)
		if err != nil {
			return err
		}
		if placed {
			if err := o.place(b.TaskID); err != nil {
				return err
			}
		}
		return o.setBlock(&b, ch)
	})
}

// DecodeOffsetForTest exposes shared offset validation before Search is implemented.
var DecodeOffsetForTest = decodeOffset

// FTSQueryForTest exposes the literal full-text query builder to external tests.
func FTSQueryForTest(user string) (string, error) { return ftsQuery(user) }

// TokenUsageForTest returns a snapshot of queued token usage without flushing it.
func (s *Service) TokenUsageForTest() map[domain.TokenID]time.Time {
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	return maps.Clone(s.usage.tokens)
}

// SetBackupHook installs a hook after attempt recording and before Snapshot.
// Configure it before starting a backup; it must not be changed concurrently.
func SetBackupHook(s *Service, fn func(ctx context.Context) error) { s.backupHook = fn }

// SetPurgeAfterAppendHook installs a hook after the pending append and before scrubbing.
// Configure it before purging; it must not be changed concurrently.
func SetPurgeAfterAppendHook(s *Service, fn func() error) { s.purgeAfterAppendHook = fn }
