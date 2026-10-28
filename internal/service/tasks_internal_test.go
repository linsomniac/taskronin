package service

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

func TestTaskLoadHelpers(t *testing.T) {
	s, m := runnerFixture(t)
	task := fixtureTask(t, s, 1)
	now := s.clock.Now().Truncate(time.Millisecond)
	entry := domain.LogEntry{TaskID: sql.Null[domain.TaskID]{V: task.ID, Valid: true}, Title: "work", Kind: domain.EntryWork,
		WorkDate: s.zone.DateOf(now), CreatedAt: now, UpdatedAt: now}
	if err := store.InsertEntry(t.Context(), s.store.Writer, &entry); err != nil {
		t.Fatal(err)
	}
	block := domain.TimeBlock{TaskID: task.ID, Span: domain.BlockSpan{Date: entry.WorkDate, Start: 780, End: 900}, Status: domain.BlockPlanned, CreatedAt: now}
	if err := store.InsertBlock(t.Context(), s.store.Writer, &block); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name         string
		load         func(*op, int64, *domain.Expect, bool) error
		id           int64
		current      any
		tombstone    func() error
		allowDeleted bool
	}{
		{"task", func(o *op, id int64, exp *domain.Expect, allow bool) error {
			_, err := o.loadTask(domain.TaskID(id), exp, allow)
			return err
		}, int64(task.ID), domain.TaskView{}, func() error {
			task.DeletedAt = sql.Null[time.Time]{V: now, Valid: true}
			return store.UpdateTask(t.Context(), s.store.Writer, task)
		}, true},
		{"entry", func(o *op, id int64, exp *domain.Expect, allow bool) error {
			_, err := o.loadEntry(domain.EntryID(id), exp, allow)
			return err
		}, int64(entry.ID), domain.EntryView{}, func() error {
			entry.DeletedAt = sql.Null[time.Time]{V: now, Valid: true}
			return store.UpdateEntry(t.Context(), s.store.Writer, entry)
		}, true},
		{"block", func(o *op, id int64, exp *domain.Expect, _ bool) error {
			_, err := o.loadBlock(domain.BlockID(id), exp)
			return err
		}, int64(block.ID), domain.BlockView{}, func() error {
			block.DeletedAt = sql.Null[time.Time]{V: now, Valid: true}
			return store.UpdateBlock(t.Context(), s.store.Writer, block)
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run := func(id int64, exp *domain.Expect, allow bool) error {
				_, err := s.run(t.Context(), m, opSpec{class: classForward}, func(o *op) error { return tt.load(o, id, exp, allow) })
				return err
			}
			for _, exp := range []*domain.Expect{nil, {Gen: s.gen, Version: 1}} {
				if err := run(tt.id, exp, false); err != nil {
					t.Fatal(err)
				}
			}
			requireRunnerCode(t, run(42, nil, false), domain.CodeNotFound)
			if de, _ := domain.AsError(run(42, nil, false)); de.Detail != tt.name+" #42 not found" {
				t.Fatalf("detail = %q", de.Detail)
			}
			requireRunnerCode(t, run(tt.id, &domain.Expect{}, false), domain.CodePreconditionReq)
			for _, exp := range []domain.Expect{{Gen: s.gen, Version: 2}, {Gen: domain.NewGeneration(), Version: 1}} {
				err := run(tt.id, &exp, false)
				requireRunnerCode(t, err, domain.CodePrecondition)
				de, _ := domain.AsError(err)
				if reflect.TypeOf(de.Current) != reflect.TypeOf(tt.current) {
					t.Fatalf("current = %T", de.Current)
				}
				switch current := de.Current.(type) {
				case domain.TaskView:
					if current.Version != 1 || current.Title != task.Title {
						t.Fatalf("current task = %+v", current)
					}
				case domain.EntryView:
					if current.Version != 1 || current.Title != entry.Title || current.TaskTitle != task.Title {
						t.Fatalf("current entry = %+v", current)
					}
				case domain.BlockView:
					if current.Version != 1 || current.TaskTitle != task.Title || current.TaskVersion != 1 {
						t.Fatalf("current block = %+v", current)
					}
				}
			}
			if err := tt.tombstone(); err != nil {
				t.Fatal(err)
			}
			requireRunnerCode(t, run(tt.id, nil, false), domain.CodeDeleted)
			if tt.allowDeleted {
				if err := run(tt.id, nil, true); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	detail, err := s.GetTask(t.Context(), m.Actor, task.ID)
	if err != nil || detail.View != domain.ViewDeleted || len(detail.Entries) != 0 || len(detail.Blocks) != 0 {
		t.Fatalf("deleted detail = %+v, %v", detail, err)
	}
}

func TestTaskReasonHelpers(t *testing.T) {
	s, m := runnerFixture(t)
	task := fixtureTask(t, s, 1)
	r, err := s.run(t.Context(), m, opSpec{class: classForward}, func(o *op) error {
		if err := o.addReason(task.ID, domain.ReasonNew); err != nil {
			return err
		}
		if err := o.addReason(task.ID, domain.ReasonManual); err != nil {
			return err
		}
		return o.addReason(task.ID, domain.ReasonManual)
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Reasons(t.Context(), s.store.Reader, task.ID)
	if err != nil || len(before) != 2 {
		t.Fatalf("reasons = %v, %v", before, err)
	}
	evs, err := store.EventsOfOp(t.Context(), s.store.Reader, r.OpID)
	if err != nil || len(evs) != 2 {
		t.Fatalf("events = %v, %v", evs, err)
	}
	r, err = s.run(t.Context(), m, opSpec{class: classForward}, func(o *op) error { return o.clearReasons(task.ID) })
	if err != nil {
		t.Fatal(err)
	}
	evs, err = store.EventsOfOp(t.Context(), s.store.Reader, r.OpID)
	if err != nil || len(evs) != 2 {
		t.Fatalf("remove events = %v, %v", evs, err)
	}
	for i, e := range evs {
		var cols map[string]any
		if err := json.Unmarshal(e.Before, &cols); err != nil {
			t.Fatal(err)
		}
		if e.Action != domain.ActRemove || len(e.After) != 0 || cols["reason"] != string(before[i].Reason) ||
			cols["since"] != float64(before[i].Since.UnixMilli()) || cols["seq"] != float64(before[i].Seq) {
			t.Fatalf("remove image = %+v", e)
		}
	}
	r, err = s.run(t.Context(), m, opSpec{class: classForward}, func(o *op) error {
		if err := o.removeReason(task.ID, domain.ReasonManual); err != nil {
			return err
		}
		return o.clearReasons(task.ID)
	})
	if err != nil || !r.NoChange {
		t.Fatalf("absent removal = %+v, %v", r, err)
	}
	task, err = store.GetTask(t.Context(), s.store.Reader, task.ID)
	if err != nil || task.Version != 3 {
		t.Fatalf("version = %d, %v", task.Version, err)
	}
	reasons, err := store.Reasons(t.Context(), s.store.Reader, task.ID)
	if err != nil || len(reasons) != 0 {
		t.Fatalf("reasons = %v, %v", reasons, err)
	}
}

func TestTaskViewDerivation(t *testing.T) {
	s, _ := runnerFixture(t)
	s.dueSoonDays = 3
	task := fixtureTask(t, s, 1)
	now := s.clock.Now()
	today := s.zone.DateOf(now)
	for _, tt := range []struct {
		name     string
		modify   func(*domain.Task)
		view     domain.View
		deferred bool
	}{
		{"backlog", func(*domain.Task) {}, domain.ViewBacklog, false},
		{"future defer", func(tk *domain.Task) { tk.DeferDate = sql.Null[civil.Date]{V: today.AddDays(1), Valid: true} }, domain.ViewUpcoming, true},
		{"elapsed defer", func(tk *domain.Task) { tk.DeferDate = sql.Null[civil.Date]{V: today, Valid: true} }, domain.ViewBacklog, false},
		{"exact defer", func(tk *domain.Task) {
			tk.DeferDate = sql.Null[civil.Date]{V: today, Valid: true}
			tk.DeferMin = sql.Null[civil.Minute]{V: 600, Valid: true}
		}, domain.ViewBacklog, false},
		{"done", func(tk *domain.Task) { tk.Status = domain.StatusDone }, domain.ViewDone, false},
		{"dropped", func(tk *domain.Task) { tk.Status = domain.StatusDropped }, domain.ViewDropped, false},
		{"deleted wins", func(tk *domain.Task) {
			tk.Status = domain.StatusDone
			tk.DeletedAt = sql.Null[time.Time]{V: now, Valid: true}
		}, domain.ViewDeleted, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tk := task
			tt.modify(&tk)
			v, err := s.taskView(t.Context(), s.store.Reader, tk, now, today)
			if err != nil || v.View != tt.view || v.Deferred != tt.deferred {
				t.Fatalf("view = %+v, %v", v, err)
			}
		})
	}
	for _, days := range []int{-1, 0, 3, 4} {
		task.DueDate = sql.Null[civil.Date]{V: today.AddDays(days), Valid: true}
		v, err := s.taskView(t.Context(), s.store.Reader, task, now, today)
		if err != nil || v.DueSoon != (days <= 3) {
			t.Fatalf("due soon (%d) = %+v, %v", days, v, err)
		}
	}
	for i, seq := range []domain.OpSeq{1, 2, 3} {
		b := domain.TimeBlock{TaskID: task.ID, Span: domain.BlockSpan{Date: today.AddDays(-i - 1), Start: 60, End: 120},
			Status: domain.BlockMissed, OutcomeSource: domain.OutcomeAuto, Slipped: true,
			SlippedAt: sql.Null[time.Time]{V: now, Valid: true}, SlippedSeq: sql.Null[domain.OpSeq]{V: seq, Valid: true},
			ResolvedAt: sql.Null[time.Time]{V: now, Valid: true}, ResolvedSeq: sql.Null[domain.OpSeq]{V: seq, Valid: true}, CreatedAt: now}
		if i == 2 {
			b.DeletedAt = sql.Null[time.Time]{V: now, Valid: true}
		}
		if err := store.InsertBlock(t.Context(), s.store.Writer, &b); err != nil {
			t.Fatal(err)
		}
	}
	for _, placed := range []sql.Null[domain.OpSeq]{{V: 99}, {V: 1, Valid: true}} {
		task.LastPlacedSeq = placed
		v, err := s.taskView(t.Context(), s.store.Reader, task, now, today)
		want := 2
		if placed.Valid {
			want = 1
		}
		if err != nil || v.SlipsLifetime != 2 || v.SlipsSinceTriage != want {
			t.Fatalf("slips = %+v, %v", v, err)
		}
	}
}

func TestBlockViewDSTAndEntryView(t *testing.T) {
	s, _ := runnerFixture(t)
	task := fixtureTask(t, s, 1)
	for _, tt := range []struct {
		date                    string
		start                   civil.Minute
		end                     civil.EndMinute
		now                     string
		started, ended, current bool
	}{
		{"2026-11-01", 90, 120, "2026-11-01T08:15:00Z", true, false, true},
		{"2026-03-08", 150, 240, "2026-03-08T09:00:00Z", true, false, true},
		{"2026-10-28", 1380, 1440, "2026-10-29T06:00:00Z", true, true, false},
	} {
		now, err := time.Parse(time.RFC3339, tt.now)
		if err != nil {
			t.Fatal(err)
		}
		b := domain.TimeBlock{TaskID: task.ID, Span: domain.BlockSpan{Date: civil.MustDate(tt.date), Start: tt.start, End: tt.end}, Status: domain.BlockPlanned}
		v, err := s.blockView(t.Context(), s.store.Reader, b, now)
		if err != nil || v.Started != tt.started || v.Ended != tt.ended || v.Current != tt.current {
			t.Fatalf("block = %+v, %v", v, err)
		}
		b.Status = domain.BlockMissed
		v, err = s.blockView(t.Context(), s.store.Reader, b, now)
		if err != nil || v.Current || v.NeedsReblock {
			t.Fatalf("resolved block = %+v, %v", v, err)
		}
	}
	for _, linked := range []bool{false, true} {
		e := domain.LogEntry{TaskID: sql.Null[domain.TaskID]{V: task.ID, Valid: linked}}
		v, err := s.entryView(t.Context(), s.store.Reader, e)
		want := ""
		if linked {
			want = task.Title
		}
		if err != nil || v.TaskTitle != want {
			t.Fatalf("entry = %+v, %v", v, err)
		}
	}
}

func TestGetTaskRelatedRecords(t *testing.T) {
	s, m := runnerFixture(t)
	task := fixtureTask(t, s, Gap)
	other := fixtureTask(t, s, 2*Gap)
	now := s.clock.Now().Truncate(time.Millisecond)
	today := s.zone.DateOf(now)
	var wantEntries []domain.EntryID
	for i := range 3 {
		e := domain.LogEntry{TaskID: sql.Null[domain.TaskID]{V: task.ID, Valid: true}, Title: "work", Kind: domain.EntryWork,
			WorkDate: today.AddDays(-i), CreatedAt: now, UpdatedAt: now}
		if i == 2 {
			e.DeletedAt = sql.Null[time.Time]{V: now, Valid: true}
		}
		if err := store.InsertEntry(t.Context(), s.store.Writer, &e); err != nil {
			t.Fatal(err)
		}
		if i != 2 {
			wantEntries = append(wantEntries, e.ID)
		}
	}
	for i := range 2 {
		b := domain.TimeBlock{TaskID: task.ID, Span: domain.BlockSpan{Date: today.AddDays(i), Start: 780, End: 900}, Status: domain.BlockPlanned, CreatedAt: now}
		if i == 1 {
			b.DeletedAt = sql.Null[time.Time]{V: now, Valid: true}
		}
		if err := store.InsertBlock(t.Context(), s.store.Writer, &b); err != nil {
			t.Fatal(err)
		}
	}
	rec := domain.OpRecord{ID: domain.NewOpID(), Kind: domain.OpFocusSet, Actor: "owner", Channel: domain.ChanCLI, At: now, State: domain.OpActive}
	if err := store.InsertOp(t.Context(), s.store.Writer, &rec); err != nil {
		t.Fatal(err)
	}
	var wantInterruptions []domain.InterruptionID
	for i := range 4 {
		in := domain.Interruption{At: now.Add(time.Duration(i) * time.Minute), Date: today,
			FromTaskID: sql.Null[domain.TaskID]{V: other.ID, Valid: true}, ToTaskID: task.ID, OpID: rec.ID}
		if i == 1 {
			in.FromTaskID.V, in.ToTaskID = task.ID, other.ID
		}
		if i == 2 {
			in.ToTaskID = other.ID
		}
		if i == 3 {
			in.DeletedAt = sql.Null[time.Time]{V: now, Valid: true}
		}
		if err := store.InsertInterruption(t.Context(), s.store.Writer, &in); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			wantInterruptions = append(wantInterruptions, in.ID)
		}
	}
	detail, err := s.GetTask(t.Context(), m.Actor, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var entries []domain.EntryID
	for _, e := range detail.Entries {
		entries = append(entries, e.ID)
		if e.TaskTitle != task.Title {
			t.Fatalf("entry = %+v", e)
		}
	}
	var interruptions []domain.InterruptionID
	for _, in := range detail.Interruptions {
		interruptions = append(interruptions, in.ID)
	}
	if !slices.Equal(entries, wantEntries) || !slices.Equal(interruptions, wantInterruptions) || len(detail.Blocks) != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	// The runner must enrich block refs with the same task metadata and flags.
	r, err := s.run(t.Context(), m, opSpec{class: classForward}, func(o *op) error {
		o.ref(domain.EntityRef{Entity: domain.EntBlock, ID: int64(detail.Blocks[0].ID)})
		return nil
	})
	if err != nil || r.Block == nil || !reflect.DeepEqual(*r.Block, detail.Blocks[0]) {
		t.Fatalf("block result = %+v, %v", r, err)
	}
}

func TestTaskRankBottomIncludesClosedAndDeleted(t *testing.T) {
	s, m := runnerFixture(t)
	closed := fixtureTask(t, s, 3*Gap)
	closed.Status = domain.StatusDropped
	if err := store.UpdateTask(t.Context(), s.store.Writer, closed); err != nil {
		t.Fatal(err)
	}
	deleted := fixtureTask(t, s, 4*Gap)
	deleted.DeletedAt = sql.Null[time.Time]{V: s.clock.Now(), Valid: true}
	if err := store.UpdateTask(t.Context(), s.store.Writer, deleted); err != nil {
		t.Fatal(err)
	}
	r, err := s.AddTask(t.Context(), m, AddTaskIn{Title: "new"})
	if err != nil || r.Task.Rank != 5*Gap {
		t.Fatalf("bottom = %+v, %v", r, err)
	}
}
