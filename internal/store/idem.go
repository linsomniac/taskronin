package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"taskronin/internal/domain"
)

// IdemRow stores a request hash and replay references, or a terminal 409 problem.
type IdemRow struct {
	Actor, Key string
	Hash       [32]byte
	OpID       sql.Null[domain.OpID]
	Status     int
	Refs       []domain.EntityRef
	ErrorJSON  []byte
	CreatedAt  time.Time
}

// AIDEV-NOTE: EntityRef has no JSON tags; the persisted A§4.9 format requires
// lowercase keys and an array even when a no-op has no entity references.
type storedEntityRef struct {
	Entity domain.EntityKind `json:"entity"`
	ID     int64             `json:"id"`
}

// GetIdem returns the actor/key record, or ErrNotFound if absent.
func GetIdem(ctx context.Context, q Q, actor, key string) (IdemRow, error) {
	var r IdemRow
	var hash []byte
	var refs string
	var problem sql.Null[string]
	var created int64
	err := q.QueryRowContext(ctx, `SELECT actor, key, request_hash, op_id, status_code,
		entity_refs, error_json, created_at FROM idempotency WHERE actor = ? AND key = ?`, actor, key).
		Scan(&r.Actor, &r.Key, &hash, &r.OpID, &r.Status, &refs, &problem, &created)
	if err != nil {
		return IdemRow{}, MapErr(err)
	}
	if len(hash) != len(r.Hash) {
		return IdemRow{}, MapErr(fmt.Errorf("store: invalid idempotency hash length"))
	}
	copy(r.Hash[:], hash)
	var stored []storedEntityRef
	if err := json.Unmarshal([]byte(refs), &stored); err != nil {
		return IdemRow{}, MapErr(fmt.Errorf("store: decode idempotency references: %w", err))
	}
	r.Refs = make([]domain.EntityRef, len(stored))
	for i, ref := range stored {
		r.Refs[i] = domain.EntityRef{Entity: ref.Entity, ID: ref.ID}
	}
	if problem.Valid {
		r.ErrorJSON = []byte(problem.V)
	}
	r.CreatedAt = time.UnixMilli(created).UTC()
	return r, nil
}

// PutIdem inserts or replaces the actor/key record.
func PutIdem(ctx context.Context, q Q, r IdemRow) error {
	refs := make([]storedEntityRef, len(r.Refs))
	for i, ref := range r.Refs {
		refs[i] = storedEntityRef{Entity: ref.Entity, ID: ref.ID}
	}
	encoded, err := json.Marshal(refs)
	if err != nil {
		return MapErr(fmt.Errorf("store: encode idempotency references: %w", err))
	}
	_, err = q.ExecContext(ctx, `INSERT OR REPLACE INTO idempotency
		(actor, key, request_hash, op_id, status_code, entity_refs, error_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, r.Actor, r.Key, r.Hash[:], r.OpID, r.Status,
		string(encoded), nullJSON(r.ErrorJSON), r.CreatedAt.UnixMilli())
	return MapErr(err)
}

// DeleteIdemBefore deletes records strictly older than before and returns the count.
func DeleteIdemBefore(ctx context.Context, q Q, before time.Time) (int64, error) {
	result, err := q.ExecContext(ctx, `DELETE FROM idempotency WHERE created_at < ?`, before.UnixMilli())
	if err != nil {
		return 0, MapErr(err)
	}
	n, err := result.RowsAffected()
	return n, MapErr(err)
}

// ClearIdempotency unconditionally deletes every stored request replay.
func ClearIdempotency(ctx context.Context, q Q) error {
	_, err := q.ExecContext(ctx, `DELETE FROM idempotency`)
	return MapErr(err)
}
