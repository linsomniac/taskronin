package service

import (
	"context"
	"encoding/json"
	"errors"

	"taskronin/internal/domain"
)

// RecordSystemOp records an offline restore or import without reconciling historical state.
// The caller supplies a content-free JSON object describing the operation.
func (s *Service) RecordSystemOp(ctx context.Context, kind domain.OpKind, after json.RawMessage) (*OpResult, error) {
	if kind != domain.OpSystemRestore && kind != domain.OpSystemImport {
		return nil, validation("kind", errors.New("must be system.restore or system.import"))
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(after, &object); err != nil || object == nil {
		return nil, validation("after", errors.New("must be a JSON object"))
	}
	return s.run(ctx, s.systemMeta(), opSpec{kind: kind, class: classForward, noReconcile: true}, func(o *op) error {
		return o.record(domain.Event{Entity: domain.EntSetting, Action: domain.ActUpdate, After: after})
	})
}
