package store

import (
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
)

func blockFixture(task domain.TaskID, date string, start, end int, status domain.BlockStatus) domain.TimeBlock {
	b := domain.TimeBlock{
		TaskID: task, Span: domain.BlockSpan{Date: civil.MustDate(date), Start: civil.Minute(start), End: civil.EndMinute(end)},
		Status: status, CreatedAt: time.UnixMilli(1793203200123).UTC(),
	}
	if status != domain.BlockPlanned {
		b.ResolvedAt, b.ResolvedSeq = present(b.CreatedAt.Add(time.Hour)), present(domain.OpSeq(7))
		if status == domain.BlockCancelled {
			b.CancelReason = domain.CancelUser
		} else {
			b.OutcomeSource = domain.OutcomeAuto
		}
	}
	return b
}

func mustInsertBlock(t *testing.T, q Q, b domain.TimeBlock) domain.TimeBlock {
	t.Helper()
	if err := InsertBlock(t.Context(), q, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func insertBlock(t *testing.T, st *Store, task domain.TaskID, date string, start, end int, status domain.BlockStatus) domain.BlockID {
	t.Helper()
	return mustInsertBlock(t, st.Writer, blockFixture(task, date, start, end, status)).ID
}

func requireBlock(t *testing.T, q Q, want domain.TimeBlock) {
	t.Helper()
	got, err := GetBlock(t.Context(), q, want.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetBlock = %+v, %v; want %+v", got, err, want)
	}
}

func requireBlockIDs(t *testing.T, got []domain.TimeBlock, err error, want ...domain.BlockID) {
	t.Helper()
	var ids []domain.BlockID
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	if err != nil || !slices.Equal(ids, want) {
		t.Fatalf("block IDs = %v, %v; want %v", ids, err, want)
	}
}

func TestBlockRoundTrip(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	for _, status := range []domain.BlockStatus{domain.BlockPlanned, domain.BlockDone, domain.BlockWorked, domain.BlockMissed, domain.BlockCancelled} {
		b := blockFixture(task.ID, "2026-10-28", 0, 1440, status)
		if status == domain.BlockMissed || status == domain.BlockCancelled {
			b.Slipped, b.SlippedAt, b.SlippedSeq = true, present(b.CreatedAt.Add(time.Hour)), present(domain.OpSeq(3))
			b.DeletedAt = present(b.CreatedAt.Add(2 * time.Hour))
		}
		if status == domain.BlockWorked {
			b.OutcomeSource = domain.OutcomeUser
		}
		b.ID, b.Version = 999, 42
		b = mustInsertBlock(t, st.Writer, b)
		if b.ID <= 0 || b.ID == 999 || b.Version != 1 {
			t.Fatalf("block ID/version = %d/%d", b.ID, b.Version)
		}
		requireBlock(t, st.Reader, b)
		var source, cancel sql.Null[string]
		var created int64
		if err := st.Reader.QueryRowContext(t.Context(), "SELECT outcome_source, cancel_reason, created_at FROM time_blocks WHERE id = ?", b.ID).Scan(&source, &cancel, &created); err != nil {
			t.Fatal(err)
		}
		if source.Valid != (b.OutcomeSource != "") || cancel.Valid != (b.CancelReason != "") || created != b.CreatedAt.UnixMilli() {
			t.Fatalf("stored source/cancel/created = %v/%v/%d", source, cancel, created)
		}
	}
	if _, err := GetBlock(t.Context(), st.Reader, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBlock missing = %v", err)
	}
	bad := blockFixture(task.ID, "2026-10-28", 600, 660, domain.BlockPlanned)
	before := bad
	var de *domain.Error
	if err := InsertBlock(t.Context(), st.Writer, &bad); !errors.As(err, &de) || de.Code != domain.CodeConflict || bad != before {
		t.Fatalf("overlap insert = %+v, %v", bad, err)
	}
}

func TestUpdateBlock(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	original := mustInsertBlock(t, st.Writer, blockFixture(task.ID, "2026-10-28", 780, 900, domain.BlockPlanned))
	want := original
	want.Span = domain.BlockSpan{Date: civil.MustDate("2026-10-29"), Start: 900, End: 1440}
	want.Status, want.OutcomeSource = domain.BlockMissed, domain.OutcomeUser
	want.Slipped, want.SlippedAt, want.SlippedSeq = true, present(want.CreatedAt.Add(time.Hour)), present(domain.OpSeq(3))
	want.ResolvedAt, want.ResolvedSeq = present(want.CreatedAt.Add(2*time.Hour)), present(domain.OpSeq(7))
	want.DeletedAt = present(want.CreatedAt.Add(3 * time.Hour))
	want.TaskID, want.Version, want.CreatedAt = 999, 99, want.CreatedAt.Add(time.Hour)
	if err := UpdateBlock(t.Context(), st.Writer, want); err != nil {
		t.Fatal(err)
	}
	want.TaskID, want.Version, want.CreatedAt = original.TaskID, original.Version, original.CreatedAt
	requireBlock(t, st.Reader, want)
	want.Status, want.OutcomeSource, want.CancelReason = domain.BlockCancelled, "", domain.CancelRescheduled
	if err := UpdateBlock(t.Context(), st.Writer, want); err != nil {
		t.Fatal(err)
	}
	requireBlock(t, st.Reader, want)
	if err := UpdateBlock(t.Context(), st.Writer, original); err != nil {
		t.Fatal(err)
	}
	requireBlock(t, st.Reader, original)
	original.ID = 999
	if err := UpdateBlock(t.Context(), st.Writer, original); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateBlock missing = %v", err)
	}
}

func TestPlannedOverlapping(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture()).ID
	b1 := insertBlock(t, st, task, "2026-10-28", 780, 900, domain.BlockPlanned)
	insertBlock(t, st, task, "2026-10-28", 600, 700, domain.BlockCancelled)
	insertBlock(t, st, task, "2026-10-29", 780, 900, domain.BlockPlanned)
	for _, tc := range []struct {
		start, end int
		want       []domain.BlockID
	}{
		{900, 960, nil}, {720, 780, nil}, {899, 960, []domain.BlockID{b1}}, {720, 781, []domain.BlockID{b1}},
		{810, 840, []domain.BlockID{b1}}, {720, 960, []domain.BlockID{b1}}, {600, 700, nil},
	} {
		span := domain.BlockSpan{Date: civil.MustDate("2026-10-28"), Start: civil.Minute(tc.start), End: civil.EndMinute(tc.end)}
		got, err := PlannedOverlapping(t.Context(), st.Reader, span)
		requireBlockIDs(t, got, err, tc.want...)
		got, err = PlannedOverlapping(t.Context(), st.Reader, span, b1)
		requireBlockIDs(t, got, err)
	}
	other := mustInsertTask(t, st.Writer, taskFixture()).ID
	b2 := insertBlock(t, st, other, "2026-10-28", 900, 960, domain.BlockPlanned)
	span := domain.BlockSpan{Date: civil.MustDate("2026-10-28"), Start: 720, End: 1000}
	got, err := PlannedOverlapping(t.Context(), st.Reader, span)
	requireBlockIDs(t, got, err, b1, b2)
	got, err = PlannedOverlapping(t.Context(), st.Reader, span, b1, b2, 999)
	requireBlockIDs(t, got, err)
}

func TestGetBlockTombstone(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture()).ID
	b := blockFixture(task, "2026-10-28", 780, 900, domain.BlockPlanned)
	b.DeletedAt = present(b.CreatedAt.Add(time.Hour))
	b = mustInsertBlock(t, st.Writer, b)
	requireBlock(t, st.Reader, b)
	got, err := BlocksBetween(t.Context(), st.Reader, b.Span.Date, b.Span.Date, sql.Null[domain.TaskID]{})
	requireBlockIDs(t, got, err)
	got, err = PlannedOn(t.Context(), st.Reader, b.Span.Date)
	requireBlockIDs(t, got, err)
	got, err = PlannedOverlapping(t.Context(), st.Reader, b.Span)
	requireBlockIDs(t, got, err)
	got, err = BlocksOfTask(t.Context(), st.Reader, task)
	requireBlockIDs(t, got, err)
	got, err = PlannedOfTask(t.Context(), st.Reader, task)
	requireBlockIDs(t, got, err)
	got, err = PlannedBefore(t.Context(), st.Reader, b.Span.Date.AddDays(1))
	requireBlockIDs(t, got, err)
	if yes, err := HasPlannedOn(t.Context(), st.Reader, task, b.Span.Date); err != nil || yes {
		t.Fatalf("tombstoned HasPlannedOn = %v, %v", yes, err)
	}
	if yes, err := HasPlannedAfter(t.Context(), st.Reader, task, b.Span.Date.AddDays(-1)); err != nil || yes {
		t.Fatalf("tombstoned HasPlannedAfter = %v, %v", yes, err)
	}
}

func TestPlannedQueries(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture()).ID
	other := mustInsertTask(t, st.Writer, taskFixture()).ID
	future := insertBlock(t, st, task, "2026-10-29", 780, 900, domain.BlockPlanned)
	today := insertBlock(t, st, task, "2026-10-28", 780, 900, domain.BlockPlanned)
	early := insertBlock(t, st, other, "2026-10-28", 600, 700, domain.BlockPlanned)
	past := insertBlock(t, st, task, "2026-10-27", 780, 900, domain.BlockPlanned)
	worked := insertBlock(t, st, task, "2026-10-28", 780, 900, domain.BlockWorked)
	day := civil.MustDate("2026-10-28")
	got, err := PlannedOn(t.Context(), st.Reader, day)
	requireBlockIDs(t, got, err, early, today)
	got, err = PlannedBefore(t.Context(), st.Reader, day)
	requireBlockIDs(t, got, err, past)
	got, err = PlannedBefore(t.Context(), st.Reader, day.AddDays(2))
	requireBlockIDs(t, got, err, past, early, today, future)
	got, err = PlannedOfTask(t.Context(), st.Reader, task)
	requireBlockIDs(t, got, err, past, today, future)
	got, err = BlocksOfTask(t.Context(), st.Reader, task)
	requireBlockIDs(t, got, err, past, today, worked, future)
	got, err = BlocksBetween(t.Context(), st.Reader, day, day.AddDays(1), sql.Null[domain.TaskID]{})
	requireBlockIDs(t, got, err, early, today, worked, future)
	got, err = BlocksBetween(t.Context(), st.Reader, day, day, present(task))
	requireBlockIDs(t, got, err, today, worked)
	got, err = BlocksBetween(t.Context(), st.Reader, civil.Date{}, civil.Date{}, present(task))
	requireBlockIDs(t, got, err, past, today, worked, future)
	for _, tc := range []struct {
		task      domain.TaskID
		date      civil.Date
		on, after bool
	}{
		{task, day, true, true}, {task, day.AddDays(1), true, false}, {task, day.AddDays(2), false, false},
		{other, day, true, false}, {999, day, false, false},
	} {
		if got, err := HasPlannedOn(t.Context(), st.Reader, tc.task, tc.date); err != nil || got != tc.on {
			t.Fatalf("HasPlannedOn = %v, %v; want %v", got, err, tc.on)
		}
		if got, err := HasPlannedAfter(t.Context(), st.Reader, tc.task, tc.date); err != nil || got != tc.after {
			t.Fatalf("HasPlannedAfter = %v, %v; want %v", got, err, tc.after)
		}
	}
}

func TestAutoResolvedOn(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture()).ID
	other := mustInsertTask(t, st.Writer, taskFixture()).ID
	var want []domain.BlockID
	for _, status := range []domain.BlockStatus{domain.BlockDone, domain.BlockWorked, domain.BlockMissed, domain.BlockCancelled, domain.BlockPlanned} {
		b := mustInsertBlock(t, st.Writer, blockFixture(task, "2026-10-28", 780, 900, status))
		if status == domain.BlockDone || status == domain.BlockWorked || status == domain.BlockMissed {
			want = append(want, b.ID)
		}
	}
	b := blockFixture(task, "2026-10-28", 780, 900, domain.BlockWorked)
	b.OutcomeSource = domain.OutcomeUser
	mustInsertBlock(t, st.Writer, b)
	b.OutcomeSource, b.DeletedAt = domain.OutcomeAuto, present(b.CreatedAt)
	mustInsertBlock(t, st.Writer, b)
	insertBlock(t, st, other, "2026-10-28", 780, 900, domain.BlockWorked)
	insertBlock(t, st, task, "2026-10-29", 780, 900, domain.BlockWorked)
	got, err := AutoResolvedOn(t.Context(), st.Reader, task, civil.MustDate("2026-10-28"))
	requireBlockIDs(t, got, err, want...)
}

func TestOtherSinceQueries(t *testing.T) {
	for _, worked := range []bool{false, true} {
		st := newTestStore(t)
		task := mustInsertTask(t, st.Writer, taskFixture()).ID
		other := mustInsertTask(t, st.Writer, taskFixture()).ID
		status := domain.BlockMissed
		if worked {
			status = domain.BlockWorked
		}
		add := func(task domain.TaskID, seq int64, deleted bool) domain.BlockID {
			b := blockFixture(task, "2026-10-28", 780, 900, status)
			b.Slipped, b.SlippedAt, b.SlippedSeq = !worked, present(b.CreatedAt), present(domain.OpSeq(seq))
			b.ResolvedSeq = present(domain.OpSeq(seq))
			// The unrelated sequence must not decide whether this block qualifies.
			if worked {
				b.SlippedSeq = present(domain.OpSeq(100))
			} else {
				b.ResolvedSeq = present(domain.OpSeq(100))
			}
			// Timestamps deliberately run opposite to sequence order.
			b.ResolvedAt = present(b.CreatedAt.Add(-time.Duration(seq) * time.Hour))
			if deleted {
				b.DeletedAt = present(b.CreatedAt)
			}
			return mustInsertBlock(t, st.Writer, b).ID
		}
		old, newer := add(task, 3, false), add(task, 7, false)
		add(task, 9, true)
		add(other, 10, false)
		inactive := blockFixture(task, "2026-10-28", 780, 900, domain.BlockCancelled)
		inactive.SlippedAt, inactive.SlippedSeq, inactive.ResolvedSeq = present(inactive.CreatedAt), present(domain.OpSeq(11)), present(domain.OpSeq(11))
		mustInsertBlock(t, st.Writer, inactive)
		for _, tc := range []struct {
			exclude domain.BlockID
			seq     int64
			want    bool
		}{
			{old, 5, true}, {newer, 5, false}, {old, 7, false}, {old, 6, true}, {newer, 3, false}, {newer, 2, true},
		} {
			var got bool
			var err error
			if worked {
				got, err = OtherWorkedSince(t.Context(), st.Reader, task, tc.exclude, tc.seq)
			} else {
				got, err = OtherSlippedSince(t.Context(), st.Reader, task, tc.exclude, tc.seq)
			}
			if err != nil || got != tc.want {
				t.Fatalf("worked=%v exclude=%d seq=%d: %v, %v; want %v", worked, tc.exclude, tc.seq, got, err, tc.want)
			}
		}
	}
}

func TestSlipCounts(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture()).ID
	other := mustInsertTask(t, st.Writer, taskFixture()).ID
	for _, row := range []struct {
		task             domain.TaskID
		seq              int64
		slipped, deleted bool
	}{
		{task, 3, true, false}, {task, 7, true, false}, {task, 9, true, true},
		{task, 11, false, false}, {other, 13, true, false},
	} {
		b := blockFixture(row.task, "2026-10-28", 780, 900, domain.BlockCancelled)
		b.Slipped, b.SlippedAt, b.SlippedSeq = row.slipped, present(b.CreatedAt), present(domain.OpSeq(row.seq))
		if row.deleted {
			b.DeletedAt = present(b.CreatedAt)
		}
		mustInsertBlock(t, st.Writer, b)
	}
	for _, tc := range []struct {
		task            domain.TaskID
		seq             int64
		lifetime, since int
	}{
		{task, 5, 2, 1}, {task, 3, 2, 1}, {task, 7, 2, 0}, {task, 0, 2, 2}, {999, 5, 0, 0},
	} {
		lifetime, since, err := SlipCounts(t.Context(), st.Reader, tc.task, tc.seq)
		if err != nil || lifetime != tc.lifetime || since != tc.since {
			t.Fatalf("slips = %d, %d, %v; want %d, %d", lifetime, since, err, tc.lifetime, tc.since)
		}
	}
}
