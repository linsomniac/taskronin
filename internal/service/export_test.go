package service

import (
	"context"
	"encoding/json"

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
