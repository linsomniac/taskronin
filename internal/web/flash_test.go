package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/web"
)

func TestFlashDuplicateCookies(t *testing.T) {
	ta := newTestApp(t).login()
	assertRedirect(t, ta.submit("/ui/", "/ui/tasks", url.Values{"title": {"First"}}), "/ui/")
	first := ta.cookies["tr_flash"]
	assertRedirect(t, ta.submit("/ui/", "/ui/tasks", url.Values{"title": {"Second"}}), "/ui/")
	second := ta.cookies["tr_flash"]
	for _, tc := range []struct{ name, cookies, want string }{
		{"valid second", "tr_flash=junk; tr_flash=" + first, "#1 created\n#1 needs triage (new)"},
		{"valid fourth", strings.Repeat("tr_flash=junk; ", 3) + "tr_flash=" + first, "#1 created\n#1 needs triage (new)"},
		{"valid fifth ignored", strings.Repeat("tr_flash=junk; ", 4) + "tr_flash=" + first, ""},
		{"first valid wins", "tr_flash=" + first + "; tr_flash=" + second, "#1 created\n#1 needs triage (new)"},
		{"bad MAC then valid", "tr_flash=YQ.YQ; tr_flash=" + second, "#2 created\n#2 needs triage (new)"},
		{"oversized then valid", "tr_flash=" + strings.Repeat("a", 2049) + "; tr_flash=" + second, "#2 created\n#2 needs triage (new)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := ta.get("/ui/tasks/1", "Cookie", "tr_session="+ta.cookies[web.SessionCookie]+"; "+tc.cookies)
			if w.Code != http.StatusOK {
				t.Fatalf("GET = %d %s", w.Code, w.Body)
			}
			if got := flashText(t, w.Body.String()); got != tc.want {
				t.Errorf("flash = %q, want %q", got, tc.want)
			}
			cookie := responseCookie(t, w, "tr_flash")
			if cookie.Path != "/ui/" || cookie.MaxAge != -1 || cookie.Value != "" {
				t.Errorf("flash clear = %+v", cookie)
			}
		})
	}
}

func TestFlashLinesEveryEffectKind(t *testing.T) {
	// DF21: literals pin the wording copied from internal/cli/effects.go.
	// Include every service.EffectKind, even kinds using the default wording.
	cases := map[service.EffectKind]struct {
		from, to, want string
	}{
		service.EffCreated:        {"", "", "#42 created"},
		service.EffUpdated:        {"", "", "#42 updated"},
		service.EffRetitled:       {"Old", "New", "#42 retitled 'Old' → 'New'"},
		service.EffPlaced:         {"", "", "#42 placed"},
		service.EffMoved:          {"", "", "#42 moved"},
		service.EffDeferred:       {"", "2026-10-30 09:00", "#42 deferred to 2026-10-30 09:00"},
		service.EffUndeferred:     {"", "", "#42 defer cleared"},
		service.EffDueSet:         {"", "2026-10-30", "#42 due 2026-10-30"},
		service.EffDueCleared:     {"", "", "#42 due date cleared"},
		service.EffBlocked:        {"", "2026-10-28 13:00-15:00", "b7 scheduled 2026-10-28 13:00–15:00"},
		service.EffBlockMoved:     {"13:00-15:00", "15:00-17:00", "b7 moved 13:00–15:00 → 15:00–17:00"},
		service.EffBlockCancelled: {"", "user", "b7 cancelled · user"},
		service.EffBlockResolved:  {"", "missed", "b7 missed"},
		service.EffOutcomeSet:     {"", "worked", "b7 marked worked"},
		service.EffSlipped:        {"", "", "b7 slipped"},
		service.EffUnslipped:      {"", "", "b7 no longer counts as slipped"},
		service.EffReasonAdded:    {"", "", "#42 needs triage (slipped)"},
		service.EffReasonRemoved:  {"", "", "#42 no longer flagged slipped"},
		service.EffFocusSet:       {"", "", "focus → #42"},
		service.EffFocusCleared:   {"", "", "focus cleared"},
		service.EffInterrupted:    {"17", "", "interrupted #17"},
		service.EffEntryLogged:    {"", "", "e9 logged"},
		service.EffEntryUpdated:   {"", "", "e9 updated"},
		service.EffEntryRelinked:  {"", "", "e9 relinked"},
		service.EffEntryDeleted:   {"", "", "e9 deleted"},
		service.EffEntryRestored:  {"", "", "e9 restored"},
		service.EffCompleted:      {"", "", "#42 completed"},
		service.EffReopened:       {"", "", "#42 reopened"},
		service.EffDropped:        {"", "", "#42 dropped"},
		service.EffDeleted:        {"", "", "#42 deleted"},
		service.EffUndeleted:      {"", "", "#42 undeleted"},
		service.EffReverted:       {"abcdef12", "", "reverted op abcdef12"},
		service.EffReviewed:       {"", "123", "AI changes reviewed through #123"},
		service.EffPurged:         {"", "", "#42 purged"},
	}
	for _, kind := range service.EffectKinds() {
		t.Run(string(kind), func(t *testing.T) {
			tc, ok := cases[kind]
			if !ok {
				t.Fatalf("missing flash wording for %q", kind)
			}
			got := web.FlashLines(&service.OpResult{Effects: []service.Effect{{
				Kind: kind, TaskID: 42, BlockID: 7, EntryID: 9, Reason: domain.ReasonSlipped, From: tc.from, To: tc.to,
			}}})
			if !slices.Equal(got, []string{tc.want}) {
				t.Errorf("flash lines = %q, want [%q]", got, tc.want)
			}
		})
	}
	for kind := range cases {
		if !slices.Contains(service.EffectKinds(), kind) {
			t.Errorf("stale flash wording for %q: not in service.EffectKinds()", kind)
		}
	}
}

func TestFlashLinesEffectVariants(t *testing.T) {
	for _, tc := range []struct {
		effect service.Effect
		want   string
	}{
		{service.Effect{Kind: service.EffBlockCancelled, BlockID: 7}, "b7 cancelled"},
		{service.Effect{Kind: service.EffBlockMoved, BlockID: 7}, "b7 moved"},
		{service.Effect{Kind: service.EffBlockResolved, BlockID: 7}, "b7 resolved"},
		{service.Effect{Kind: service.EffReverted, Via: "undo", From: "abcdef12", To: "task.complete"}, "undid complete (op abcdef12)"},
		{service.Effect{Kind: service.EffReverted, Via: "redo", From: "abcdef13", To: "entry.add"}, "redid log (op abcdef13)"},
		{service.Effect{Kind: service.EffReverted, Via: "revert", From: "abcdef14", To: "task.update"}, "reverted edit (op abcdef14)"},
		{service.Effect{Kind: service.EffReverted, From: "abcdef15", To: "future.kind"}, "reverted future.kind (op abcdef15)"},
		{service.Effect{Kind: service.EffReverted, Via: "undo", From: "abcdef16"}, "undone (op abcdef16)"},
		{service.Effect{Kind: service.EffReverted, Via: "redo", From: "abcdef17"}, "redone (op abcdef17)"},
	} {
		got := web.FlashLines(&service.OpResult{Effects: []service.Effect{tc.effect}})
		if !slices.Equal(got, []string{tc.want}) {
			t.Errorf("effect %+v: got %q, want [%q]", tc.effect, got, tc.want)
		}
	}
}

func TestFlashLinesWarningsAndFlags(t *testing.T) {
	got := web.FlashLines(&service.OpResult{
		Effects:  []service.Effect{{Kind: service.EffUpdated, TaskID: 42}},
		Warnings: []string{"first warning", "second warning"}, NoChange: true, Replayed: true,
	})
	want := []string{"#42 updated", "first warning", "second warning", "no change", "already applied"}
	if !slices.Equal(got, want) {
		t.Errorf("flash lines = %q, want %q", got, want)
	}
}

func TestMutateEmptyFlashPreservesPending(t *testing.T) {
	ta := newTestApp(t).login()
	form := scrapeForm(t, ta.viewBody("/ui/"), "/ui/tasks")
	form.Set("title", "Pending flash")
	assertRedirect(t, ta.post("/ui/tasks", form), "/ui/")
	r := httptest.NewRequestWithContext(web.WithSession(t.Context(), web.SessionInfo{
		Actor: webOwner(), CSRF: form.Get("_csrf"),
	}), http.MethodPost, "/ui/probe", nil)
	r.PostForm = form
	r.AddCookie(&http.Cookie{Name: "tr_flash", Value: ta.cookies["tr_flash"]})
	w := httptest.NewRecorder()
	ta.App.Mutate(w, r, func(service.Meta, url.Values) (*service.OpResult, error) {
		return &service.OpResult{}, nil
	})
	assertRedirect(t, w, "/ui/")
	if got := w.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("empty result overwrote pending flash: %v", got)
	}
	if got := flashText(t, ta.viewBody("/ui/")); got != "#1 created\n#1 needs triage (new)" {
		t.Errorf("pending flash = %q", got)
	}
}
