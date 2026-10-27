package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// UpdateSettingsIn changes optional retention counts, requiring a password to lower either.
type UpdateSettingsIn struct {
	KeepDaily, KeepWeekly *int
	Password              string
}

// UpdateSettings applies retention floors and records password-authorized reductions.
func (s *Service) UpdateSettings(ctx context.Context, m Meta, in UpdateSettingsIn) (SettingsView, error) {
	var view SettingsView
	_, err := s.run(ctx, m, opSpec{kind: domain.OpSettingsUpdate, class: classForward,
		scopes: domain.ScopeAdmin}, func(o *op) error {
		if in.KeepDaily != nil && *in.KeepDaily < 3 {
			return validation("keep_daily", errors.New("must be at least 3"))
		}
		if in.KeepWeekly != nil && *in.KeepWeekly < 1 {
			return validation("keep_weekly", errors.New("must be at least 1"))
		}
		daily, err := store.GetSetting(o.ctx, o.tx, "backup_keep_daily")
		if err != nil {
			return err
		}
		weekly, err := store.GetSetting(o.ctx, o.tx, "backup_keep_weekly")
		if err != nil {
			return err
		}
		view.KeepDaily, err = strconv.Atoi(daily)
		if err != nil {
			return err
		}
		view.KeepWeekly, err = strconv.Atoi(weekly)
		if err != nil {
			return err
		}
		lowering := (in.KeepDaily != nil && *in.KeepDaily < view.KeepDaily) ||
			(in.KeepWeekly != nil && *in.KeepWeekly < view.KeepWeekly)
		if lowering {
			if in.Password == "" {
				return domain.Errorf(domain.CodePasswordRequired, "password is required to lower retention")
			}
			a, err := store.GetAuthState(o.ctx, o.tx)
			if err != nil {
				return err
			}
			if err := o.verifyPassword(a, in.Password); err != nil {
				return err
			}
			// AIDEV-NOTE: Decide from settings under the writer lock. A read
			// before run would let concurrent raises bypass password re-entry.
			o.rec.Kind = domain.OpSettingsRetentionLowered
			if err := store.SetOpKind(o.ctx, o.tx, o.rec.ID, o.rec.Kind); err != nil {
				return err
			}
		}
		for _, setting := range []struct {
			key, before string
			input       *int
			value       *int
		}{
			{"backup_keep_daily", daily, in.KeepDaily, &view.KeepDaily},
			{"backup_keep_weekly", weekly, in.KeepWeekly, &view.KeepWeekly},
		} {
			if setting.input == nil || *setting.input == *setting.value {
				continue
			}
			after := strconv.Itoa(*setting.input)
			if err := store.SetSetting(o.ctx, o.tx, setting.key, after); err != nil {
				return err
			}
			bj, err := json.Marshal(map[string]string{"key": setting.key, "value": setting.before})
			if err != nil {
				return err
			}
			aj, err := json.Marshal(map[string]string{"key": setting.key, "value": after})
			if err != nil {
				return err
			}
			if err := o.record(domain.Event{Entity: domain.EntSetting, Action: domain.ActUpdate, Before: bj, After: aj}); err != nil {
				return err
			}
			*setting.value = *setting.input
		}
		return nil
	})
	if err != nil {
		return SettingsView{}, err
	}
	return view, nil
}
