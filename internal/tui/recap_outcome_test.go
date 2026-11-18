package tui

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

func recapFixture(date civil.Date) api.Day {
	return api.Day{
		Date: date,
		Blocks: []api.Block{
			{ID: 1, TaskID: 1, TaskTitle: "cluster", TaskETag: "g-1", ETag: "b-1", Date: date, Start: 780, End: 900, Status: "missed", OutcomeSource: ptr("auto"), Slipped: true},
			{ID: 2, TaskID: 2, TaskTitle: "checks", TaskETag: "g-2", ETag: "b-2", Date: date, Start: 540, End: 600, Status: "worked", OutcomeSource: ptr("auto")},
		},
		Entries:  []api.Entry{{ID: 5, TaskID: ptr(int64(2)), TaskTitle: ptr("checks"), Title: "x", WorkDate: date, ETag: "e-5"}},
		Unlinked: []api.Entry{{ID: 6, Title: "Answered Bob's DNS question", WorkDate: date, ETag: "e-6"}},
		Interruptions: []api.Interruption{{
			At:   api.Time{Time: testClock().Now.AddDate(0, 0, date.DaysSince(testClock().Today)).Add(20 * time.Minute)},
			Date: date, FromTaskID: ptr(int64(42)), FromBlockID: ptr(int64(9)), ToTaskID: 60,
		}},
	}
}

func recapApp(t *testing.T, f *fakeAPI, date civil.Date) *App {
	t.Helper()
	return newTestApp(t, f, func(c *Config) { c.StartMode, c.RecapDate = ModeRecap, date })
}

func historicalRecap(t *testing.T) (*App, *fakeAPI, civil.Date) {
	t.Helper()
	f := newFake()
	date := civil.MustDate("2026-10-27")
	f.days[date] = recapFixture(date)
	return recapApp(t, f, date), f, date
}

func TestRecapOutcomeKeys(t *testing.T) { // BLK-44, BLK-45, BLK-49, BLK-50 (TUI side).
	a, f, date := historicalRecap(t)
	for _, tc := range []struct {
		key, outcome string
		id           int64
	}{{"w", "worked", 1}, {"m", "missed", 2}, {"a", "auto", 2}} {
		if tc.key == "m" {
			press(t, a, "j")
		}
		reads, todayReads := f.count("Day"), f.count("Today")
		press(t, a, tc.key)
		eq(t, f.mustLast(t, "SetOutcome"), call{Method: "SetOutcome", ID: tc.id, Mut: client.Mut{IfMatch: fmt.Sprintf("b-%d", tc.id)}, Req: api.OutcomeRequest{Outcome: tc.outcome}})
		eq(t, f.count("Day"), reads+1)
		eq(t, f.count("Today"), todayReads+1)
		eq(t, f.mustLast(t, "Day").Q, date)
		if a.form != nil {
			t.Fatal("outcome key opened a form")
		}
	}
	eq(t, f.count("AddTask"), 0)
}

func TestRecapStartedTodayBlock(t *testing.T) { // BLK-47/R9 (TUI side).
	for _, k := range []string{"w", "m"} {
		t.Run(k, func(t *testing.T) {
			f := newFake()
			date := f.clock.Today
			d := recapFixture(date)
			d.Blocks[0].Status, d.Blocks[0].OutcomeSource = "planned", nil
			f.days[date] = d
			a := recapApp(t, f, date)
			press(t, a, k)
			eq(t, f.count("SetOutcome"), 0)
			eq(t, a.banner.text, "not started — use unblock")
			d.Blocks = slices.Clone(d.Blocks)
			d.Blocks[0].Started = true
			f.days[date] = d
			drain(t, a, a.refresh())
			press(t, a, k)
			eq(t, f.count("SetOutcome"), 1)
			eq(t, f.mustLast(t, "SetOutcome").Mut, client.Mut{IfMatch: "b-1"})
		})
	}
}

func TestRecapDoneTask(t *testing.T) {
	a, f, _ := historicalRecap(t)
	reads := f.count("Day")
	press(t, a, "x")
	eq(t, f.mustLast(t, "CompleteTask"), call{Method: "CompleteTask", ID: 1, Mut: client.Mut{IfMatch: "g-1"}, Req: api.CompleteRequest{}})
	eq(t, f.count("GetTask"), 0)
	eq(t, f.count("Day"), reads+1)
}

func TestRecapMutationErrorPolicy(t *testing.T) { // CF03: an unknown outcome can wrap a 412.
	for _, tc := range []struct {
		err     error
		text    string
		refresh bool
	}{
		{problem("precondition_failed", 412, ""), "changed elsewhere — refreshed", true},
		{problem("generation_mismatch", 412, ""), "database was restored — refreshed", true},
		{notSent(), "cannot reach the Taskronin server — nothing was saved", false},
		{&client.OutcomeUnknownError{Key: "recap-key", Err: problem("precondition_failed", 412, "")}, "outcome unknown — refreshed; check before repeating", true},
	} {
		t.Run(tc.text, func(t *testing.T) {
			a, f, _ := historicalRecap(t)
			before := f.count("Day")
			f.errs["SetOutcome"] = tc.err
			press(t, a, "w")
			eq(t, f.count("SetOutcome"), 1)
			eq(t, a.banner.text, tc.text)
			if tc.refresh {
				before++
			}
			eq(t, f.count("Day"), before)
		})
	}
}

func TestRecapMutationCapturesAndGuards(t *testing.T) {
	for _, k := range []string{"w", "x"} {
		t.Run(k, func(t *testing.T) {
			a, f, _ := historicalRecap(t)
			_, cmd := a.Update(key(k))
			press(t, a, "j", k, "l", "L")
			eq(t, a.banner.text, "busy…")
			if a.form != nil {
				t.Fatal("opened a form during a pending mutation")
			}
			drain(t, a, cmd)
			method, etag := "SetOutcome", "b-1"
			if k == "x" {
				method, etag = "CompleteTask", "g-1"
			}
			eq(t, f.count(method), 1)
			eq(t, f.mustLast(t, method).ID, int64(1))
			eq(t, f.mustLast(t, method).Mut.IfMatch, etag)
		})
	}
}
