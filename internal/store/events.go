package store

import (
	"context"
	"database/sql"

	"taskronin/internal/domain"
)

const eventColumns = `events.id, events.op_id, events.entity, events.entity_id, events.task_id,
	events.action, events.before_json, events.after_json, events.derived`

type eventScan struct {
	event         domain.Event
	before, after sql.Null[string]
}

func (s *eventScan) fields() []any {
	e := &s.event
	return []any{&e.ID, &e.OpID, &e.Entity, &e.EntityID, &e.TaskID, &e.Action, &s.before, &s.after, &e.Derived}
}

func (s *eventScan) value() domain.Event {
	if s.before.Valid {
		s.event.Before = []byte(s.before.V)
	}
	if s.after.Valid {
		s.event.After = []byte(s.after.V)
	}
	return s.event
}

func nullJSON(b []byte) sql.Null[string] {
	return sql.Null[string]{V: string(b), Valid: len(b) > 0}
}

// InsertEvent inserts a caller-supplied event, assigning its generated ID.
// Empty before/after payloads are stored as SQL NULL.
func InsertEvent(ctx context.Context, q Q, e *domain.Event) error {
	var id domain.EventID
	err := q.QueryRowContext(ctx, `INSERT INTO events
		(op_id, entity, entity_id, task_id, action, before_json, after_json, derived)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		e.OpID, e.Entity, e.EntityID, e.TaskID, e.Action, nullJSON(e.Before), nullJSON(e.After), e.Derived).Scan(&id)
	if err != nil {
		return MapErr(err)
	}
	e.ID = id
	return nil
}

// EventsOfOp returns an operation's events in ascending event ID order.
func EventsOfOp(ctx context.Context, q Q, op domain.OpID) ([]domain.Event, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+eventColumns+` FROM events WHERE op_id = ? ORDER BY id`, op)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var events []domain.Event
	for rows.Next() {
		var s eventScan
		if err := rows.Scan(s.fields()...); err != nil {
			return nil, MapErr(err)
		}
		events = append(events, s.value())
	}
	return events, MapErr(rows.Err())
}

// EventKey identifies an entity; Reason further distinguishes triage rows only.
type EventKey struct {
	Entity domain.EntityKind
	ID     int64
	Reason domain.TriageReason
}

// EventOp pairs an event with the operation that recorded it.
type EventOp struct {
	domain.Event
	Op domain.OpRecord
}

// EventsAfter returns all events on k strictly after afterID, ordered by event ID.
// Operation state and derived events are deliberately left for the caller to interpret.
func EventsAfter(ctx context.Context, q Q, k EventKey, afterID domain.EventID) ([]EventOp, error) {
	// AIDEV-NOTE: Removed triage reasons exist only in the before-image. Both
	// payloads participate in the key; other entities ignore Reason entirely.
	return queryEventOps(ctx, q, `SELECT `+eventColumns+`, `+opColumns+`
		FROM events JOIN ops ON ops.op_id = events.op_id
		WHERE events.entity = ?1 AND events.entity_id = ?2 AND events.id > ?3
		AND (?1 <> 'triage' OR json_extract(events.after_json, '$.reason') = ?4
			OR json_extract(events.before_json, '$.reason') = ?4) ORDER BY events.id`,
		k.Entity, k.ID, afterID, k.Reason)
}

// TaskEvents returns a task's denormalized history, newest first; zero beforeID is unbounded.
func TaskEvents(ctx context.Context, q Q, task domain.TaskID, beforeID domain.EventID, limit int) ([]EventOp, error) {
	return queryEventOps(ctx, q, `SELECT `+eventColumns+`, `+opColumns+`
		FROM events JOIN ops ON ops.op_id = events.op_id
		WHERE events.task_id = ?1 AND (?2 = 0 OR events.id < ?2) ORDER BY events.id DESC LIMIT ?3`,
		task, beforeID, limit)
}

// queryEventOps accepts constant SQL only; filter values are bound parameters.
func queryEventOps(ctx context.Context, q Q, query string, args ...any) ([]EventOp, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var events []EventOp
	for rows.Next() {
		var e eventScan
		var o opScan
		if err := rows.Scan(append(e.fields(), o.fields()...)...); err != nil {
			return nil, MapErr(err)
		}
		op, err := o.value()
		if err != nil {
			return nil, err
		}
		events = append(events, EventOp{Event: e.value(), Op: op})
	}
	return events, MapErr(rows.Err())
}

// OpTaskIDs returns the distinct non-NULL task IDs on an operation's events, ascending.
func OpTaskIDs(ctx context.Context, q Q, op domain.OpID) ([]domain.TaskID, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT task_id FROM events
		WHERE op_id = ? AND task_id IS NOT NULL ORDER BY task_id`, op)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var ids []domain.TaskID
	for rows.Next() {
		var id domain.TaskID
		if err := rows.Scan(&id); err != nil {
			return nil, MapErr(err)
		}
		ids = append(ids, id)
	}
	return ids, MapErr(rows.Err())
}
