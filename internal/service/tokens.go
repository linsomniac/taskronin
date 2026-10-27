package service

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
	"taskronin/internal/textsafe"
)

// CreateTokenIn describes a named non-owner token and its limits.
type CreateTokenIn struct {
	Name             string
	Scopes           domain.Scopes
	ExpiresAt        sql.Null[time.Time]
	MutationsPerHour int
}

// TokenCreated returns public metadata and the bearer secret shown only once.
type TokenCreated struct {
	Token  domain.Token
	Secret string
}

// RevokeTokenIn identifies a non-owner token to revoke.
type RevokeTokenIn struct{ ID domain.TokenID }

// AuthenticateToken checks a bearer and records successful use in memory.
func (s *Service) AuthenticateToken(ctx context.Context, bearer string, verifiedSocket bool, ch domain.Channel, remoteKey string) (domain.Actor, error) {
	id, raw, err := auth.ParseToken(bearer)
	if err != nil {
		return domain.Actor{}, domain.Errorf(domain.CodeUnauthenticated, "invalid token")
	}
	token, err := store.GetToken(ctx, s.store.Reader, id)
	found := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return domain.Actor{}, serviceError(err)
	}
	hash := auth.HashSecret(raw)
	// AIDEV-NOTE: Unknown IDs also compare a full digest (against the zero
	// row's dummy hash); authentication failures expose no token state.
	match := subtle.ConstantTimeCompare(hash[:], token.SecretHash[:])
	now := s.clock.Now().Truncate(time.Millisecond)
	if match != 1 || !found || token.RevokedAt.Valid || (token.ExpiresAt.Valid && !token.ExpiresAt.V.After(now)) {
		return domain.Actor{}, domain.Errorf(domain.CodeUnauthenticated, "invalid token")
	}
	a := domain.Actor{Kind: domain.ActorToken, TokenID: token.ID, TokenName: token.Name,
		Scopes: token.Scopes, Channel: ch, MutationsPerHour: token.MutationsPerHour, RemoteKey: remoteKey}
	if token.IsOwner {
		// AIDEV-NOTE: Owner tokens require verifiedSocket, independently of
		// the requested channel. MCP may never authenticate as the owner.
		if !verifiedSocket {
			return domain.Actor{}, domain.Errorf(domain.CodeUnauthenticated, "invalid token")
		}
		if ch == domain.ChanMCP {
			return domain.Actor{}, domain.Errorf(domain.CodeForbidden, "owner tokens are not permitted for MCP")
		}
		a.Kind, a.TokenName, a.Scopes, a.MutationsPerHour = domain.ActorOwner, "", domain.AllScopes, 0
	}
	s.noteTokenUse(id, now)
	return a, nil
}

// CreateToken creates a non-revertible token; retries cannot retrieve its secret.
func (s *Service) CreateToken(ctx context.Context, m Meta, in CreateTokenIn) (TokenCreated, error) {
	var created TokenCreated
	_, err := s.run(ctx, m, opSpec{kind: domain.OpTokenCreate, class: classForward,
		scopes: domain.ScopeAdmin, notWeb: true}, func(o *op) error {
		if err := textsafe.ValidateName(in.Name, 64); err != nil {
			return validation("name", err)
		}
		if strings.HasPrefix(in.Name, "owner.") {
			return validation("name", errors.New("owner. prefix is reserved"))
		}
		if in.Scopes == 0 || in.Scopes & ^domain.AllScopes != 0 {
			return validation("scopes", errors.New("must contain at least one scope and only declared scopes"))
		}
		if in.ExpiresAt.Valid {
			in.ExpiresAt.V = in.ExpiresAt.V.UTC().Truncate(time.Millisecond)
			if !in.ExpiresAt.V.After(o.now) {
				return validation("expires_at", errors.New("must be in the future"))
			}
		}
		if in.MutationsPerHour < 0 {
			return validation("mutations_per_hour", errors.New("must not be negative"))
		}
		raw, err := auth.NewSecret()
		if err != nil {
			return err
		}
		token := domain.Token{Name: in.Name, SecretHash: auth.HashSecret(raw), Scopes: in.Scopes,
			ExpiresAt: in.ExpiresAt, MutationsPerHour: in.MutationsPerHour, CreatedAt: o.now.UTC()}
		if err := store.InsertToken(o.ctx, o.tx, &token); err != nil {
			return err
		}
		if err := o.recordToken(token, domain.ActInsert); err != nil {
			return err
		}
		token.SecretHash = [32]byte{}
		created = TokenCreated{Token: token, Secret: auth.FormatToken(token.ID, raw)}
		return nil
	})
	if err != nil {
		return TokenCreated{}, err
	}
	return created, nil
}

// ListTokens returns all token metadata with secret hashes removed.
func (s *Service) ListTokens(ctx context.Context, a domain.Actor) ([]domain.Token, error) {
	if a.Channel == domain.ChanWeb {
		return nil, domain.Errorf(domain.CodeForbidden, "token management is not permitted on the web")
	}
	var tokens []domain.Token
	err := s.read(ctx, a, domain.ScopeAdmin, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		var err error
		tokens, err = store.ListTokens(ctx, q)
		for i := range tokens {
			tokens[i].SecretHash = [32]byte{}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return tokens, nil
}

// RevokeToken revokes a non-owner token, or returns its unchanged revoked metadata.
func (s *Service) RevokeToken(ctx context.Context, m Meta, in RevokeTokenIn) (domain.Token, error) {
	r, err := s.run(ctx, m, opSpec{kind: domain.OpTokenRevoke, class: classForward,
		scopes: domain.ScopeAdmin, notWeb: true, idem: true}, func(o *op) error {
		token, err := store.GetToken(o.ctx, o.tx, in.ID)
		if err != nil {
			return err
		}
		if token.IsOwner {
			return domain.Errorf(domain.CodeConflict, "rotate owner tokens with an offline password reset")
		}
		o.ref(domain.EntityRef{Entity: domain.EntToken, ID: int64(token.ID)})
		if token.RevokedAt.Valid {
			return nil
		}
		if err := store.RevokeToken(o.ctx, o.tx, token.ID, o.now); err != nil {
			return err
		}
		return o.recordToken(token, domain.ActRevoke)
	})
	if err != nil {
		return domain.Token{}, err
	}
	if r.Token == nil {
		return domain.Token{}, domain.Errorf(domain.CodeInternal, "missing token result reference")
	}
	return *r.Token, nil
}

func (o *op) recordToken(token domain.Token, action domain.Action) error {
	// AIDEV-NOTE: Explicitly allowlist audit metadata; never marshal a Token
	// or request containing a secret/hash into events or idempotency storage.
	after, err := json.Marshal(map[string]any{"name": token.Name, "scopes": token.Scopes.String(), "expires_at": instantCol(token.ExpiresAt)})
	if err != nil {
		return err
	}
	return o.record(domain.Event{Entity: domain.EntToken, EntityID: int64(token.ID), Action: action, After: after})
}
