package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

func aiOp(seq int64) api.OpSummary {
	return api.OpSummary{Seq: seq, OpID: fmt.Sprintf("op-%d", seq), At: testClock().Now,
		Kind: "entry.add", Actor: "token:ai", State: "active", Revertible: true}
}

func aiApp(t *testing.T, pages ...api.AIChanges) (*App, *fakeAPI) {
	t.Helper()
	f := newFake()
	f.aiPages = pages
	a := newTestApp(t, f)
	press(t, a, "A")
	if f.count("AIChanges") != 1 || len(a.overlays) != 1 {
		t.Fatal("A did not open AI changes")
	}
	eq(t, f.mustLast(t, "AIChanges"), call{Method: "AIChanges", Q: false, Req: client.PageQuery{Limit: 50}})
	return a, f
}

func TestAIChanges(t *testing.T) { // SCP-11, UND-11 (TUI side).
	a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(12), aiOp(10)}})
	if !strings.Contains(viewText(a), "12 10:00 entry.add by token:ai") {
		t.Fatal(viewText(a))
	}
	f.aiPages[0].Ops[0].State = "reverted"
	press(t, a, "R")
	eq(t, f.mustLast(t, "Revert"), call{Method: "Revert", Q: "op-12", Mut: client.Mut{}})
	eq(t, f.count("AIChanges"), 2)
	eq(t, f.count("Today"), 2)
	if !strings.Contains(viewText(a), " [reverted]") {
		t.Fatal(viewText(a))
	}
	press(t, a, "A")
	eq(t, f.mustLast(t, "ReviewAIChanges"), call{Method: "ReviewAIChanges", Req: api.ReviewRequest{ThroughSeq: 12}})
	eq(t, f.count("ReviewAIChanges"), 1)
	for _, mode := range []Mode{ModeTriage, ModePlan} {
		f = newFake()
		f.tasks["triage"] = []api.Task{task(42, "new task", "new")}
		a = newTestApp(t, f, func(c *Config) { c.StartMode = mode })
		press(t, a, "A", "y")
		eq(t, f.count("AcceptNew"), 1)
		eq(t, f.count("AIChanges"), 0)
	}
}

func TestAIReviewNeverCoversUnseen(t *testing.T) {
	ops := make([]api.OpSummary, 50)
	for i := range ops {
		ops[i] = aiOp(101 - int64(i))
	}
	a, f := aiApp(t, api.AIChanges{Ops: ops, NextCursor: ptr("c2")}, api.AIChanges{Ops: []api.OpSummary{aiOp(51)}})
	press(t, a, "A")
	eq(t, f.count("ReviewAIChanges"), 0)
	eq(t, f.mustLast(t, "AIChanges").Req, client.PageQuery{Limit: 50, Cursor: "c2"})
	eq(t, a.banner.text, "loaded all 51 unreviewed changes — review them, then press A again")
	// CF02: both clock-only and payload-changing polls preserve the review flow.
	for _, changed := range []bool{false, true} {
		td := *a.today
		td.Clock.Now.Time = td.Clock.Now.Add(5 * time.Second)
		if changed {
			td.TriageCount++
		}
		f.setToday(td, fmt.Sprint(changed))
		drain(t, a, func() tea.Msg { return pollMsg{} })
		eq(t, f.count("AIChanges"), 2)
		eq(t, a.banner.text, "loaded all 51 unreviewed changes — review them, then press A again")
	}
	for range 50 {
		press(t, a, "j")
	}
	if !strings.Contains(viewText(a), "› 51 10:00") {
		t.Fatal("last change is unreachable", viewText(a))
	}
	_, review := a.Update(key("A"))
	press(t, a, "A", "R")
	eq(t, a.banner.text, "busy…")
	eq(t, f.count("ReviewAIChanges"), 0)
	drain(t, a, review)
	eq(t, f.count("ReviewAIChanges"), 1)
	eq(t, f.mustLast(t, "ReviewAIChanges").Req, api.ReviewRequest{ThroughSeq: 101})
}

func TestAIReviewHighestAndEmpty(t *testing.T) {
	a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(10), aiOp(12), aiOp(11)}, ReviewedThrough: 100, Unreviewed: 99})
	press(t, a, "A")
	eq(t, f.mustLast(t, "ReviewAIChanges").Req, api.ReviewRequest{ThroughSeq: 12})
	a, f = aiApp(t, api.AIChanges{Unreviewed: 100})
	press(t, a, "A", "R", "j", "k")
	eq(t, f.count("ReviewAIChanges")+f.count("Revert"), 0)
	if !strings.Contains(viewText(a), "no unreviewed AI changes") {
		t.Fatal(viewText(a))
	}
}

func TestAIReviewPartialFailure(t *testing.T) {
	for _, tc := range []struct {
		err    error
		banner string
	}{
		{notSent(), "cannot reach the Taskronin server"},
		{outcomeUnknown(""), "server did not answer"},
	} {
		t.Run(tc.banner, func(t *testing.T) {
			a, f := aiApp(t,
				api.AIChanges{Ops: []api.OpSummary{aiOp(12)}, NextCursor: ptr("c2")},
				api.AIChanges{Ops: []api.OpSummary{aiOp(10)}, NextCursor: ptr("c3")},
				api.AIChanges{Ops: []api.OpSummary{aiOp(8)}},
			)
			_, second := a.Update(key("A"))
			if second == nil {
				t.Fatal("no load-all command")
			}
			_, third := a.Update(second())
			f.errs["AIChanges"] = tc.err
			drain(t, a, third)
			eq(t, f.count("AIChanges"), 3)
			eq(t, f.count("ReviewAIChanges"), 0)
			eq(t, f.count("Today"), 1)
			eq(t, a.banner.text, tc.banner)
			eq(t, a.banner.readError, true)
			if !strings.Contains(viewText(a), "10 10:00") {
				t.Fatal("lost successful pages")
			}
			press(t, a, "A") // Retry the unread tail; still no review.
			eq(t, f.count("ReviewAIChanges"), 0)
			eq(t, f.mustLast(t, "AIChanges").Req, client.PageQuery{Limit: 50, Cursor: "c3"})
			eq(t, a.banner.text, "loaded all 3 unreviewed changes — review them, then press A again")
			press(t, a, "A")
			eq(t, f.mustLast(t, "ReviewAIChanges").Req, api.ReviewRequest{ThroughSeq: 12})
		})
	}
}

func TestAIRevertResetsReview(t *testing.T) {
	a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(12), aiOp(10)}, NextCursor: ptr("c2")}, api.AIChanges{Ops: []api.OpSummary{aiOp(8)}})
	press(t, a, "A", "R")
	eq(t, f.count("AIChanges"), 3)
	eq(t, f.mustLast(t, "AIChanges").Req, client.PageQuery{Limit: 50})
	press(t, a, "A")
	eq(t, f.count("ReviewAIChanges"), 0)
	eq(t, f.count("AIChanges"), 4)
	press(t, a, "A")
	eq(t, f.count("ReviewAIChanges"), 1)
}

func TestAIPagingGuardAndStaleness(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(12), aiOp(10)}, NextCursor: ptr("c2")}, api.AIChanges{Ops: []api.OpSummary{aiOp(8)}})
			f.aiCursors = []string{"", "c2", "c3"} // A failed c2 read still names the second fixture.
			_, page := a.Update(key("down"))
			if page == nil {
				t.Fatal("last row did not request next page")
			}
			press(t, a, "j", "k", "j", "A")
			eq(t, f.count("AIChanges"), 1)
			eq(t, a.banner.text, "busy…")
			if fail {
				f.errs["AIChanges"] = outcomeUnknown("")
			}
			stale := page()
			f.aiPages[0] = api.AIChanges{Ops: []api.OpSummary{aiOp(14)}, NextCursor: ptr("c3")}
			f.aiPages = append(f.aiPages, api.AIChanges{Ops: []api.OpSummary{aiOp(6)}})
			press(t, a, "u") // Undo can supersede a pending append; R now waits for it.
			eq(t, f.count("Undo"), 1)
			before, banner := f.count("AIChanges"), a.banner.text
			press(t, a, "j")
			eq(t, f.count("AIChanges"), before)
			drain(t, a, func() tea.Msg { return stale })
			eq(t, a.banner.text, banner)
			if strings.Contains(viewText(a), "\n  8 10:00") || strings.Contains(viewText(a), "› 8 10:00") {
				t.Fatal("stale page appended")
			}
			press(t, a, "j")
			eq(t, f.mustLast(t, "AIChanges").Req, client.PageQuery{Limit: 50, Cursor: "c3"})
			press(t, a, "j", "R")
			eq(t, f.mustLast(t, "Revert").Q, "op-6")
		})
	}
}

func TestAIReviewLoadsEveryPage(t *testing.T) {
	a, f := aiApp(t,
		api.AIChanges{Ops: []api.OpSummary{aiOp(12)}, NextCursor: ptr("c2")},
		api.AIChanges{Ops: []api.OpSummary{aiOp(10)}, NextCursor: ptr("c3")},
		api.AIChanges{Ops: []api.OpSummary{aiOp(8)}, NextCursor: ptr("c4")},
		api.AIChanges{Ops: []api.OpSummary{aiOp(6)}},
	)
	press(t, a, "A")
	eq(t, f.count("AIChanges"), 4)
	eq(t, f.count("ReviewAIChanges"), 0)
	eq(t, a.banner.text, "loaded all 4 unreviewed changes — review them, then press A again")
	press(t, a, "j", "j", "j", "R")
	eq(t, f.mustLast(t, "Revert").Q, "op-6")
}

func TestAIInitialLoadBusyAndFailure(t *testing.T) {
	f := newFake()
	f.aiPages = []api.AIChanges{{Ops: []api.OpSummary{aiOp(12)}}}
	a := newTestApp(t, f)
	_, load := a.Update(key("A"))
	if load == nil {
		t.Fatal("missing initial load")
	}
	press(t, a, "A", "R", "j")
	eq(t, len(a.overlays), 1)
	eq(t, a.banner.text, "busy…")
	eq(t, f.count("AIChanges")+f.count("ReviewAIChanges")+f.count("Revert"), 0)
	f.errs["AIChanges"] = outcomeUnknown("")
	drain(t, a, load)
	eq(t, a.banner.text, "server did not answer")
	eq(t, f.count("AIChanges"), 1)
	press(t, a, "A")
	eq(t, f.count("AIChanges"), 2)
	eq(t, f.count("ReviewAIChanges"), 0)
	press(t, a, "A")
	eq(t, f.count("ReviewAIChanges"), 1)
}

func TestAIMutationErrorPolicy(t *testing.T) {
	for _, k := range []string{"A", "R"} {
		for _, tc := range []struct {
			err     error
			banner  string
			refresh bool
		}{
			{notSent(), "cannot reach the Taskronin server — nothing was saved", false},
			{problem("generation_mismatch", 412, ""), "database was restored — refreshed", true},
			{&client.OutcomeUnknownError{Err: problem("precondition_failed", 412, "")}, "outcome unknown — refreshed; check before repeating", true},
		} {
			t.Run(k+"/"+tc.banner, func(t *testing.T) {
				a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(12)}})
				method := "ReviewAIChanges"
				if k == "R" {
					method = "Revert"
				}
				f.errs[method] = tc.err
				press(t, a, k)
				eq(t, f.count(method), 1)
				eq(t, a.banner.text, tc.banner)
				eq(t, f.count("AIChanges") == 2, tc.refresh)
			})
		}
	}
}

func TestAILoadFailureCannotReviewOldList(t *testing.T) {
	a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(12)}})
	f.errs["AIChanges"] = notSent()
	press(t, a, "R") // Revert succeeded but the replacement page failed.
	eq(t, a.banner.text, "cannot reach the Taskronin server")
	press(t, a, "A")
	eq(t, f.count("ReviewAIChanges"), 0)
	eq(t, f.count("AIChanges"), 3)
	press(t, a, "A")
	eq(t, f.count("ReviewAIChanges"), 1)
}

func TestAILoadCapturesInputsAndCreationOrder(t *testing.T) {
	a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(12)}})
	old, latest := a.current().load(a), a.current().load(a)
	f.aiPages[0] = api.AIChanges{Ops: []api.OpSummary{aiOp(15)}}
	drain(t, a, latest)
	other := newFake()
	a.api = other
	f.errs["AIChanges"] = outcomeUnknown("")
	drain(t, a, old)
	eq(t, other.count("AIChanges"), 0)
	eq(t, a.banner.text, "")
	if !strings.Contains(viewText(a), "15 10:00") {
		t.Fatal("stale result overwrote list")
	}
}

func TestAIClipsAndSanitizes(t *testing.T) {
	op := aiOp(12)
	op.Kind = "kind\x1b\u009b\u202e"
	op.Actor = "actor\x1b\u009b\u202e" + strings.Repeat("宽", 100)
	a, _ := aiApp(t, api.AIChanges{Ops: []api.OpSummary{op}})
	for _, color := range []bool{false, true} {
		a.styles = newStyles(color)
		for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 30}, {Width: 20, Height: 5}, {Width: 1, Height: 1}, {}} {
			a.Update(size)
			v := viewText(a)
			if strings.ContainsAny(v, "\u009b\u202e") || (!color && strings.ContainsRune(v, '\x1b')) {
				t.Fatalf("unsafe output %q", v)
			}
			for _, line := range strings.Split(v, "\n") {
				if lipgloss.Width(line) > size.Width {
					t.Fatalf("oversized line %q", line)
				}
			}
		}
	}
}

func TestAIStackedLoadsRemainIndependent(t *testing.T) { // M1.
	for _, appendPage := range []bool{false, true} {
		for _, lowerFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("append=%v/lowerFirst=%v", appendPage, lowerFirst), func(t *testing.T) {
				f := newFake()
				f.aiPages = []api.AIChanges{
					{Ops: []api.OpSummary{aiOp(12), aiOp(10)}, NextCursor: ptr("c2")},
					{Ops: []api.OpSummary{aiOp(8)}},
				}
				a := newTestApp(t, f)
				_, lowerLoad := a.Update(key("A"))
				lower := a.current()
				if appendPage {
					drain(t, a, lowerLoad)
					_, lowerLoad = a.Update(key("j"))
				}
				searchFor(t, a, "between overlays")
				// Build a synthetic stack to retain reply-isolation coverage; the
				// public A binding now refuses to stack AI overlays (P17).
				upperLoad := a.openOverlay(newAIOverlay())
				if lowerFirst {
					drain(t, a, lowerLoad)
					drain(t, a, upperLoad)
				} else {
					drain(t, a, upperLoad)
					drain(t, a, lowerLoad)
				}
				press(t, a, "esc", "esc")
				eq(t, a.current() == lower, true)
				want := "12 10:00"
				if appendPage {
					want = "8 10:00"
				}
				if !strings.Contains(lower.view(a, 100, 25), want) {
					t.Fatal("lower overlay lost its reply", viewText(a))
				}
				press(t, a, "A")
				if a.banner.text == "busy…" {
					t.Fatal("lower overlay remained busy")
				}
				if !appendPage {
					eq(t, f.count("ReviewAIChanges"), 0)
					press(t, a, "A")
				}
				eq(t, f.count("ReviewAIChanges"), 1)
				eq(t, f.mustLast(t, "ReviewAIChanges").Req, api.ReviewRequest{ThroughSeq: 12})
			})
		}
	}
}

func TestAIStackedFailedLoadReleasesGuard(t *testing.T) { // M1.
	f := newFake()
	f.aiPages = []api.AIChanges{{Ops: []api.OpSummary{aiOp(12)}}}
	a := newTestApp(t, f)
	_, lower := a.Update(key("A"))
	searchFor(t, a, "between overlays")
	drain(t, a, a.openOverlay(newAIOverlay())) // Synthetic stack; A no longer opens over overlays.
	f.errs["AIChanges"] = notSent()
	drain(t, a, lower)
	press(t, a, "esc", "esc")
	eq(t, a.banner.text, "cannot reach the Taskronin server")
	press(t, a, "A")
	eq(t, f.count("AIChanges"), 3)
	eq(t, f.count("ReviewAIChanges"), 0)
	press(t, a, "A")
	eq(t, f.count("ReviewAIChanges"), 1)
}

func TestAIRevealedAfterOwnOpReloadsFirstPage(t *testing.T) { // M2.
	for _, k := range []string{"u", "ctrl+r"} {
		t.Run(k, func(t *testing.T) {
			a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(12), aiOp(10)}, NextCursor: ptr("c2")}, api.AIChanges{Ops: []api.OpSummary{aiOp(8)}})
			base := &recScreen{}
			a.setTabScreen(TabToday, base)
			press(t, a, "A") // Fully loaded before another screen's own operation.
			searchFor(t, a, "upper")
			f.aiPages[0].Ops = []api.OpSummary{aiOp(14), aiOp(12)}
			press(t, a, k)
			eq(t, f.count("AIChanges"), 2)
			press(t, a, "esc")
			eq(t, f.count("AIChanges"), 3)
			eq(t, f.mustLast(t, "AIChanges").Req, client.PageQuery{Limit: 50})
			if !strings.Contains(viewText(a), "14 10:00") {
				t.Fatal("revealed stale AI rows")
			}
			eq(t, a.overlayDirty, true)
			eq(t, base.loads, 0)
			press(t, a, "A") // Reload discarded the previously loaded tail.
			eq(t, f.count("ReviewAIChanges"), 0)
			eq(t, f.mustLast(t, "AIChanges").Req, client.PageQuery{Limit: 50, Cursor: "c2"})
			press(t, a, "esc")
			eq(t, base.loads, 1)
			eq(t, a.overlayDirty, false)
		})
	}
}

func TestAIFailedReloadHidesStaleRows(t *testing.T) { // M3.
	a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(12)}})
	f.errs["AIChanges"] = notSent()
	press(t, a, "R")
	v := viewText(a)
	if !strings.Contains(v, "AI changes not loaded · r retries · A retries") {
		t.Fatal("missing retry hint", v)
	}
	if strings.Contains(v, "12 10:00") || strings.Contains(v, "R revert") || strings.Contains(v, "A review") {
		t.Fatal("failed reload displays current-looking rows/actions", v)
	}
	_, retry := a.Update(key("A"))
	if out := viewText(a); !strings.Contains(out, "loading AI changes…") || strings.Contains(out, "not loaded") {
		t.Fatal("pending retry must show loading", out)
	}
	press(t, a, "A", "R")
	eq(t, a.banner.text, "busy…")
	drain(t, a, retry)
	if !strings.Contains(viewText(a), "12 10:00") || !strings.Contains(viewText(a), "R revert · A review") {
		t.Fatal("retry did not restore rows/actions", viewText(a))
	}
}

func TestAIRevertWaitsForPage(t *testing.T) { // M4.
	for _, k := range []string{"A", "j"} {
		t.Run(k, func(t *testing.T) {
			a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(12)}, NextCursor: ptr("c2")}, api.AIChanges{Ops: []api.OpSummary{aiOp(10)}})
			_, page := a.Update(key(k))
			f.errs["Revert"] = notSent()
			press(t, a, "R")
			eq(t, f.count("Revert"), 0)
			eq(t, a.banner.text, "busy…")
			drain(t, a, page)
			press(t, a, "R")
			eq(t, f.count("Revert"), 1)
			eq(t, a.banner.text, "cannot reach the Taskronin server — nothing was saved")
		})
	}
}

func TestFinalAILoadingIsNotFailure(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	load := a.openOverlay(newAIOverlay())
	if out := viewText(a); !strings.Contains(out, "loading AI changes…") || strings.Contains(out, "not loaded") || strings.Contains(out, "retries") {
		t.Fatalf("pending initial load looks failed: %s", out)
	}
	f.errs["AIChanges"] = notSent()
	drain(t, a, load)
	out := viewText(a)
	for _, hint := range []string{"AI changes not loaded · r retries", "A retries"} {
		if !strings.Contains(out, hint) {
			t.Fatalf("missing %q: %s", hint, out)
		}
	}
	_, retry := a.Update(key("A"))
	if out := viewText(a); strings.Contains(out, "not loaded") || !strings.Contains(out, "loading AI changes…") {
		t.Fatal(out)
	}
	drain(t, a, retry)
}

func TestAIRevealedAfterPollRetainsTail(t *testing.T) {
	a, f := aiApp(t, api.AIChanges{Ops: []api.OpSummary{aiOp(12)}, NextCursor: ptr("c2")}, api.AIChanges{Ops: []api.OpSummary{aiOp(8)}})
	press(t, a, "A")
	searchFor(t, a, "upper")
	td := *a.today
	td.AIChanges++
	f.setToday(td, "changed")
	drain(t, a, a.poll())
	press(t, a, "esc")
	eq(t, f.count("AIChanges"), 2)
	if !strings.Contains(viewText(a), "8 10:00") {
		t.Fatal(viewText(a))
	}
	press(t, a, "A")
	eq(t, f.mustLast(t, "ReviewAIChanges").Req, api.ReviewRequest{ThroughSeq: 12})
}
