package store

import (
	"slices"
	"testing"
	"time"

	"taskronin/internal/domain"
)

func TestReplaceTokens(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	old := tokenFixture(t, "old")
	if err := InsertToken(ctx, st.Writer, &old); err != nil {
		t.Fatal(err)
	}
	owner, revoked, ai := tokenFixture(t, "owner.test"), tokenFixture(t, "revoked"), tokenFixture(t, "ai")
	owner.ID, owner.IsOwner, owner.Scopes, owner.MutationsPerHour = 3, true, domain.AllScopes, 0
	revoked.ID, revoked.RevokedAt = 5, present(revoked.CreatedAt.Add(time.Second))
	ai.ID, ai.ExpiresAt, ai.LastUsedAt = 7, present(ai.CreatedAt.Add(time.Hour)), present(ai.CreatedAt.Add(time.Minute))
	want := []domain.Token{owner, revoked, ai}

	tx, err := st.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ReplaceTokens(ctx, tx, want); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := ListTokens(ctx, st.Reader)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("tokens = %+v, %v; want %+v", got, err, want)
	}
	var seq int64
	if err := st.Reader.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'api_tokens'`).Scan(&seq); err != nil || seq < 7 {
		t.Fatalf("api_tokens sequence = %d, %v", seq, err)
	}

	// An empty replacement leaves the table empty.
	if err := ReplaceTokens(ctx, st.Writer, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := ListTokens(ctx, st.Reader); err != nil || len(got) != 0 {
		t.Fatalf("after empty replace: %+v, %v", got, err)
	}
}
