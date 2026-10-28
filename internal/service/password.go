package service

import (
	"context"
	"encoding/json"

	"taskronin/internal/auth"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// ChangePasswordIn supplies the current and replacement owner passwords.
type ChangePasswordIn struct{ Current, New string }

// ResetPasswordIn supplies a replacement password and a freshly minted owner secret.
type ResetPasswordIn struct {
	NewPassword string
	OwnerSecret [32]byte
}

// ChangePassword verifies the owner password and invalidates all web sessions.
func (s *Service) ChangePassword(ctx context.Context, m Meta, in ChangePasswordIn) error {
	// AIDEV-NOTE: Password bytes are never logged or stored outside auth_state;
	// password-bearing operations are idempotency-exempt, even with Meta.Idem.
	// AIDEV-NOTE: The gate wait and both argon2 runs happen BEFORE run opens the
	// writer tx; fn only compares the auth_state seen here and writes.
	if !m.Actor.Scopes.Has(domain.ScopeAdmin) || m.Actor.Kind != domain.ActorOwner {
		return domain.Errorf(domain.CodeForbidden, "operation is not permitted")
	}
	seen, err := store.GetAuthState(ctx, s.store.Reader)
	if err != nil {
		return serviceError(err)
	}
	ok, err := s.passwords.Verify(ctx, m.Actor.RemoteKey, seen.PasswordHash, in.Current)
	if err != nil {
		return serviceError(err)
	}
	if !ok {
		return domain.Errorf(domain.CodePasswordInvalid, "invalid password")
	}
	if err := auth.ValidateNewPassword(in.New); err != nil {
		return validation("new", err)
	}
	newHash, err := auth.HashPassword(in.New)
	if err != nil {
		return err
	}
	_, err = s.run(ctx, m, opSpec{kind: domain.OpAuthPassword, class: classForward,
		scopes: domain.ScopeAdmin, ownerOnly: true}, func(o *op) error {
		a, err := store.GetAuthState(o.ctx, o.tx)
		if err != nil {
			return err
		}
		if a.PasswordHash != seen.PasswordHash || a.PasswordChangedAt != seen.PasswordChangedAt {
			return domain.Errorf(domain.CodeConflict, "password changed concurrently; retry")
		}
		a.PasswordHash = newHash
		a.PasswordChangedAt = o.now
		if err := store.PutAuthState(o.ctx, o.tx, a); err != nil {
			return err
		}
		if _, err := store.DeleteAllSessions(o.ctx, o.tx); err != nil {
			return err
		}
		return o.recordPasswordChange()
	})
	return err
}

// ResetPasswordOffline rotates all credentials atomically; callers must hold the offline instance lock.
func (s *Service) ResetPasswordOffline(ctx context.Context, in ResetPasswordIn) (domain.TokenID, error) {
	var id domain.TokenID
	_, err := s.run(ctx, s.systemMeta(), opSpec{kind: domain.OpAuthPassword, class: classForward}, func(o *op) error {
		if err := auth.ValidateNewPassword(in.NewPassword); err != nil {
			return validation("new_password", err)
		}
		hash, err := auth.HashPassword(in.NewPassword)
		if err != nil {
			return err
		}
		a, err := store.GetAuthState(o.ctx, o.tx)
		if err != nil {
			return err
		}
		a.PasswordHash, a.PasswordChangedAt, a.WebLoginDisabled = hash, o.now, false
		if err := store.PutAuthState(o.ctx, o.tx, a); err != nil {
			return err
		}
		ids, err := store.RevokeAllTokens(o.ctx, o.tx, o.now)
		if err != nil {
			return err
		}
		for _, tokenID := range ids {
			token, err := store.GetToken(o.ctx, o.tx, tokenID)
			if err != nil {
				return err
			}
			if err := o.recordToken(token, domain.ActRevoke); err != nil {
				return err
			}
		}
		if _, err := store.DeleteAllSessions(o.ctx, o.tx); err != nil {
			return err
		}
		name, err := auth.OwnerTokenName(o.now)
		if err != nil {
			return err
		}
		token := domain.Token{Name: name, SecretHash: auth.HashSecret(in.OwnerSecret), Scopes: domain.AllScopes,
			IsOwner: true, CreatedAt: o.now}
		if err := store.InsertToken(o.ctx, o.tx, &token); err != nil {
			return err
		}
		if err := o.recordToken(token, domain.ActInsert); err != nil {
			return err
		}
		id = token.ID
		return o.recordPasswordChange()
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (o *op) verifyPassword(a store.AuthState, password string) error {
	ok, err := o.s.passwords.Verify(o.ctx, o.actor.RemoteKey, a.PasswordHash, password)
	if err != nil {
		return err
	}
	if !ok {
		return domain.Errorf(domain.CodePasswordInvalid, "invalid password")
	}
	return nil
}

func (o *op) recordPasswordChange() error {
	return o.record(domain.Event{Entity: domain.EntAuth, EntityID: 1, Action: domain.ActUpdate,
		After: json.RawMessage(`{"what":"password_changed"}`)})
}
