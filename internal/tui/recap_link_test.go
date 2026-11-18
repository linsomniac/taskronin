package tui

import (
	"strings"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

func TestRecapLinkUnlinked(t *testing.T) { // RCP-09 (TUI side).
	for _, value := range []string{"1", "#1", "abc", "-"} {
		t.Run(value, func(t *testing.T) {
			a, f, _ := historicalRecap(t)
			press(t, a, "j", "j", "L")
			fm, ok := a.form.(*form)
			if !ok {
				t.Fatal("missing link form")
			}
			for _, want := range []string{"#1 cluster", "#2 checks"} {
				if !strings.Contains(fm.hint, want) {
					t.Fatalf("hint %q lacks %q", fm.hint, want)
				}
			}
			reads := f.count("Day")
			typeText(t, a, value)
			press(t, a, "enter")
			if value == "abc" || value == "-" {
				eq(t, f.count("RelinkEntry"), 0)
				eq(t, f.count("Generation"), 0)
				if fm.errText == "" {
					t.Fatal("missing form error")
				}
				return
			}
			eq(t, f.mustLast(t, "RelinkEntry"), call{Method: "RelinkEntry", ID: 6, Mut: client.Mut{Key: freshFormKey(t, f, "RelinkEntry"), IfMatch: "e-6"}, Req: api.RelinkRequest{TaskID: api.Opt[int64]{Set: true, V: 1}}})
			eq(t, f.count("Day"), reads+1)
		})
	}
}

func TestRecapBackdatedLogNeedsChoice(t *testing.T) { // FOC-07 (TUI side).
	for _, choice := range []string{"-", "2", "#2"} {
		t.Run(choice, func(t *testing.T) {
			a, f, date := historicalRecap(t)
			f.today.Focus = ptr(task(42, "today's focus"))
			press(t, a, "j", "j", "l")
			if !strings.Contains(viewText(a), "task # or - for none") {
				t.Fatalf("missing historical task picker: %s", viewText(a))
			}
			reads := f.count("Day")
			typeText(t, a, choice)
			press(t, a, "enter")
			eq(t, f.count("Generation"), 0)
			eq(t, f.count("AddEntry"), 0)
			eq(t, f.count("Day"), reads)
			if a.inflight {
				t.Fatal("task picker reserved a mutation")
			}
			fm, ok := a.form.(*form)
			if !ok || !strings.HasPrefix(fm.title, "Log → ") {
				t.Fatal("picker did not open log form")
			}
			want := api.Opt[int64]{Set: true, V: 2}
			if choice == "-" {
				want = api.Opt[int64]{Set: true, Null: true}
			}
			typeText(t, a, "x")
			press(t, a, "enter")
			eq(t, f.mustLast(t, "AddEntry").Req, api.AddEntryRequest{TaskID: want, Title: "x", WorkDate: &date})
			eq(t, f.count("Day"), reads+1)
		})
	}
	t.Run("selected block", func(t *testing.T) {
		a, f, date := historicalRecap(t)
		press(t, a, "l")
		if !strings.Contains(viewText(a), "Log → #1 (selected)") {
			t.Fatal(viewText(a))
		}
		typeText(t, a, "x")
		press(t, a, "enter")
		eq(t, f.mustLast(t, "AddEntry").Req, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, V: 1}, Title: "x", WorkDate: &date})
	})
}

func TestRecapPickerValidationAndCancel(t *testing.T) {
	a, f, _ := historicalRecap(t)
	press(t, a, "j", "j", "l")
	typeText(t, a, "abc")
	press(t, a, "enter")
	if !strings.Contains(viewText(a), "expected a task ID") {
		t.Fatal(viewText(a))
	}
	eq(t, f.count("AddEntry"), 0)
	eq(t, f.count("Generation"), 0)
	press(t, a, "esc")
	if a.form != nil || a.quitting {
		t.Fatal("escape should close only the picker")
	}
}

func TestRecapTodayLogImplicitLink(t *testing.T) {
	for _, tc := range []struct {
		name  string
		focus *api.Task
		block *api.Block
		want  api.Opt[int64]
	}{
		{"focus", ptr(task(42, "focus")), &api.Block{TaskID: 60}, api.Opt[int64]{Set: true, V: 42}},
		{"current block", nil, &api.Block{TaskID: 60}, api.Opt[int64]{Set: true, V: 60}},
		{"no task", nil, nil, api.Opt[int64]{Set: true, Null: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.days[f.clock.Today] = recapFixture(f.clock.Today)
			f.today.Focus, f.today.CurrentBlock = tc.focus, tc.block
			a := recapApp(t, f, civil.Date{})
			press(t, a, "down", "down", "l")
			if !strings.Contains(viewText(a), "Log → ") || strings.Contains(viewText(a), "task # or - for none") {
				t.Fatal(viewText(a))
			}
			typeText(t, a, "x")
			press(t, a, "enter")
			eq(t, f.mustLast(t, "AddEntry").Req, api.AddEntryRequest{TaskID: tc.want, Title: "x"})
		})
	}
}

func TestRecapFormsKeepRetrySnapshots(t *testing.T) {
	for _, k := range []string{"L", "l"} {
		t.Run(k, func(t *testing.T) {
			a, f, date := historicalRecap(t)
			press(t, a, "j", "j", k)
			method := "RelinkEntry"
			if k == "l" {
				press(t, a, "2", "enter")
				method = "AddEntry"
			}
			typeText(t, a, "1")
			f.errs[method] = &client.OutcomeUnknownError{Key: "recap-retry", Err: problem("precondition_failed", 412, "")}
			press(t, a, "enter")
			first := f.mustLast(t, method)
			fm, ok := a.form.(*form)
			if !ok || fm.retry == nil || fm.input.Value() != "1" {
				t.Fatal("form lost text or retry snapshot")
			}
			d := recapFixture(date)
			d.Unlinked[0].ETag = "e-6-new"
			f.days[date] = d
			f.clock.Today = f.clock.Today.AddDays(1)
			f.clock.Now.Time = f.clock.Now.Add(24 * time.Hour)
			f.today.Clock, f.today.Focus = f.clock, ptr(task(99, "new focus"))
			drain(t, a, a.refresh())
			_, cmd := a.Update(key("enter"))
			press(t, a, "enter")
			eq(t, a.banner.text, "busy…")
			drain(t, a, cmd)
			first.Mut.Key = "recap-retry"
			eq(t, f.mustLast(t, method), first)
			eq(t, f.count(method), 2)
		})
	}
}
