package web_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"taskronin/internal/datespec"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
	"taskronin/internal/web"
)

func TestExpect(t *testing.T) {
	ta := newTestApp(t)
	got, err := ta.App.Expect(url.Values{"etag": {`"0123456789abcdef-3"`}}, "etag")
	if err != nil || got != (domain.Expect{Gen: "0123456789abcdef", Version: 3}) {
		t.Fatalf("Expect = %+v, %v", got, err)
	}
	for _, form := range []url.Values{{}, {"etag": {""}}} {
		_, err := ta.App.Expect(form, "etag")
		testutil.RequireCode(t, err, domain.CodePreconditionReq)
	}
	for _, value := range []string{`W/"0123456789abcdef-3"`, "*"} {
		_, err := ta.App.Expect(url.Values{"etag": {value}}, "etag")
		testutil.RequireCode(t, err, domain.CodeMalformed)
	}
}

func TestSafeReturn(t *testing.T) {
	for _, value := range []string{"/ui/", "/ui/backlog", "/ui/log?from=2026-10-20&to=2026-10-28", "/ui/tasks/42#entries", "/ui/" + strings.Repeat("a", 2044)} {
		if got := web.SafeReturn(value); got != value {
			t.Errorf("SafeReturn(%q) = %q", value, got)
		}
	}
	for _, value := range []string{"", "https://evil.example/", "//evil.example/ui/", `/\evil.example`, "javascript:alert(1)",
		"/api/v1/tasks", "/ui/../api/v1/tasks", "/static/app.css", "/ui/\r\nSet-Cookie: x=1", "ui/backlog",
		"/ui/%2e%2e/api/v1/tasks", "/ui/%5cevil", "/ui/%0d%0aX:1", "/ui/\ttab", `\/evil`, "%2f%2fevil", "/ui/..",
		"/ui/" + strings.Repeat("a", 2045)} {
		if got := web.SafeReturn(value); got != "/ui/" {
			t.Errorf("SafeReturn(%q) = %q, want fallback", value, got)
		}
	}
}

func TestIdemBody(t *testing.T) {
	form := url.Values{"_csrf": {"x"}, "_nonce": {"n"}, "b": {"2"}, "a": {"1"}, "tags": {"z", "y"}, "_gen": {"g"}, "password": {"pw-SECRET-zz9"}}
	before := form.Encode()
	if got := string(web.IdemBody(form)); got != "_gen=g&a=1&b=2&tags=z&tags=y" {
		t.Errorf("IdemBody = %q", got)
	}
	if form.Encode() != before {
		t.Error("IdemBody changed the form")
	}
}

func TestMoreURL(t *testing.T) {
	u, err := url.Parse("/ui/log?from=2026-10-20&to=2026-10-21&include_deleted=1&cursor=old&tag=a&tag=b")
	if err != nil {
		t.Fatal(err)
	}
	before := u.String()
	got, err := url.Parse(web.MoreURL(u, "abc"))
	if err != nil {
		t.Fatal(err)
	}
	want := u.Query()
	want.Set("cursor", "abc")
	if got.Path != u.Path || !reflect.DeepEqual(got.Query(), want) || u.String() != before {
		t.Fatalf("MoreURL = %s; original = %s", got, u)
	}
	if got := web.MoreURL(&url.URL{Path: "/ui/backlog"}, "x"); got != "/ui/backlog?cursor=x" {
		t.Errorf("MoreURL = %q", got)
	}
}

func TestMeta(t *testing.T) {
	ta := newTestApp(t)
	nonce := strings.Repeat("a", 32)
	form := url.Values{"_nonce": {nonce}, "_gen": {"00000000000000aa"}, "_csrf": {"x"}, "_return": {"/ui/"}, "title": {"a"}}
	r := httptest.NewRequestWithContext(web.WithSession(t.Context(), web.SessionInfo{Actor: webOwner()}), http.MethodPost, "/ui/tasks", nil)
	r.PostForm = form
	m, err := ta.App.Meta(r)
	if err != nil || m.Actor != webOwner() || m.Gen != "00000000000000aa" || m.Idem.Key != nonce || m.Idem.Method != http.MethodPost || m.Idem.Route != "POST /ui/tasks" || string(m.Idem.Body) != "_gen=00000000000000aa&_return=%2Fui%2F&title=a" {
		t.Fatalf("Meta = %+v, body = %s, err = %v", m, m.Idem.Body, err)
	}
	for _, value := range []string{"", "invalid"} {
		r.PostForm = url.Values{"_nonce": {value}, "_gen": {value}}
		m, err := ta.App.Meta(r)
		if err != nil || m.Idem.Key != value || string(m.Gen) != value {
			t.Fatalf("Meta must leave nonce/gen validation to service: %+v, %v", m, err)
		}
	}
	r = r.WithContext(t.Context())
	_, err = ta.App.Meta(r)
	testutil.RequireCode(t, err, domain.CodeUnauthenticated)
}

func TestPathInt(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ui/tasks/42", nil)
	r.SetPathValue("id", "42")
	if id, err := web.PathInt(r, "id"); err != nil || id != 42 {
		t.Fatalf("PathInt = %d, %v", id, err)
	}
	for _, raw := range []string{"", "0", "-1", "+42", "#42", "4x", "9223372036854775808"} {
		r.SetPathValue("id", raw)
		_, err := web.PathInt(r, "id")
		testutil.RequireCode(t, err, domain.CodeNotFound)
	}
}

func TestFormDecoders(t *testing.T) {
	ta := newTestApp(t)
	f := func(s string) url.Values { return url.Values{"value": {s}} }
	m, err := ta.App.FormMoment(f("fri 9:00"), "value", datespec.Future)
	if err != nil || m == nil || *m != testutil.Moment("2026-10-30 09:00") {
		t.Fatalf("FormMoment = %+v, %v", m, err)
	}
	if m, err := ta.App.FormMoment(f(""), "value", datespec.Future); err != nil || m != nil {
		t.Errorf("empty FormMoment = %+v, %v", m, err)
	}
	for _, tc := range []struct {
		input, date string
		start, end  int
	}{
		{"13:00-15:00", "2026-10-28", 780, 900}, {"fri 23:00-24:00", "2026-10-30", 1380, 1440},
	} {
		span, err := ta.App.FormSpan(f(tc.input), "value")
		if err != nil || span.Date != testutil.D(tc.date) || int(span.Start) != tc.start || int(span.End) != tc.end {
			t.Errorf("FormSpan(%q) = %+v, %v", tc.input, span, err)
		}
	}
	d, err := ta.App.FormDate(f("yesterday"), "value", datespec.Past)
	if err != nil || d != testutil.D("2026-10-27") {
		t.Errorf("FormDate = %v, %v", d, err)
	}
	if d, err := ta.App.FormDate(f(""), "value", datespec.Past); err != nil || !d.IsZero() {
		t.Errorf("empty FormDate = %v, %v", d, err)
	}
	minute, err := web.FormMinute(f("9:05"), "value")
	if err != nil || !minute.Valid || minute.V != 545 {
		t.Errorf("FormMinute = %+v, %v", minute, err)
	}
	if minute, err := web.FormMinute(f(""), "value"); err != nil || minute.Valid {
		t.Errorf("empty FormMinute = %+v, %v", minute, err)
	}
	_, momentErr := ta.App.FormMoment(f("someday"), "value", datespec.Future)
	_, spanErr := ta.App.FormSpan(f("15:00-13:00"), "value")
	_, dateErr := ta.App.FormDate(f("2026-10-27 09:00"), "value", datespec.Past)
	_, minuteErr := web.FormMinute(f("24:00"), "value")
	for _, err := range []error{momentErr, spanErr, dateErr, minuteErr} {
		testutil.RequireCode(t, err, domain.CodeValidation)
		de, _ := domain.AsError(err)
		if len(de.Fields) != 1 || de.Fields[0].Field != "value" {
			t.Errorf("field error = %+v", de)
		}
	}
	if !strings.Contains(dateErr.Error(), "date only") {
		de, _ := domain.AsError(dateErr)
		if !strings.Contains(de.Fields[0].Message, "date only") {
			t.Errorf("date error = %+v", de)
		}
	}
	for _, input := range []string{"42", "#42"} {
		if id, err := web.FormInt(f(input), "value"); err != nil || id != 42 {
			t.Errorf("FormInt(%q) = %d, %v", input, id, err)
		}
	}
	for _, input := range []string{"0", "-1", "4x", "", "+42", "9223372036854775808"} {
		_, err := web.FormInt(f(input), "value")
		testutil.RequireCode(t, err, domain.CodeMalformed)
		de, _ := domain.AsError(err)
		if len(de.Fields) != 1 || de.Fields[0].Field != "value" {
			t.Errorf("id field error = %+v", de)
		}
	}
	if got := web.SplitTags("a, b  c,,"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("SplitTags = %v", got)
	}
}

func TestFormUnrecognisedDateMessage(t *testing.T) {
	ta := newTestApp(t)
	ta.login()
	w := ta.submit("/ui/triage", "/ui/tasks", url.Values{"title": {"X"}, "due": {"next monday"}})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	want := `unrecognised date "next": use today, yesterday, tomorrow, a weekday (mon…sun), +Nd/-Nd, +Nw/-Nw, or YYYY-MM-DD`
	requireFieldErrors(t, w.Body.String(), "due")
	if !strings.Contains(nodeText(parseHTML(t, w.Body.String())), want) {
		t.Fatalf("date field error missing: %s", w.Body)
	}
}

type formPages struct {
	*testApp
	triage, middle, focus, deferred, due, done, dropped, deleted domain.TaskID
}

func newFormPages(t *testing.T) formPages {
	t.Helper()
	ta := newTestApp(t).login()
	p := formPages{testApp: ta}
	p.triage = ta.addTask(service.AddTaskIn{Title: "x", Tags: []string{"docs"}})
	for _, title := range []string{"First", "Middle", "Last"} {
		id := ta.addTask(service.AddTaskIn{Title: title, Place: &service.AddPlacement{How: "bottom"}})
		if title == "Middle" {
			p.middle = id
		}
	}
	p.focus = ta.addTask(service.AddTaskIn{Title: "Focus", Tags: []string{"docs"}})
	p.deferred = ta.addTask(service.AddTaskIn{Title: "Deferred", Place: &service.AddPlacement{How: "defer", Defer: testutil.Moment("10-30 09:00")}})
	p.due = ta.addTask(service.AddTaskIn{Title: "Due soon", Due: ptrMoment(testutil.Moment("10-29 13:00"))})
	p.done = ta.addTask(service.AddTaskIn{Title: "Done"})
	ta.mustOp(ta.Svc.Complete(t.Context(), ta.meta(), service.CompleteIn{ID: p.done, Expect: ta.taskExpect(p.done)}))
	p.dropped = ta.addTask(service.AddTaskIn{Title: "Dropped"})
	ta.mustOp(ta.Svc.Drop(t.Context(), ta.meta(), service.TaskRef{ID: p.dropped, Expect: ta.taskExpect(p.dropped)}))
	p.deleted = ta.addTask(service.AddTaskIn{Title: "Deleted"})
	ta.mustOp(ta.Svc.Delete(t.Context(), ta.meta(), service.TaskRef{ID: p.deleted, Expect: ta.taskExpect(p.deleted)}))

	focus := sql.Null[domain.TaskID]{V: p.focus, Valid: true}
	ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: p.focus, Expect: ta.taskExpect(p.focus),
		Span: domain.BlockSpan{Date: testutil.D("10-28"), Start: 13 * 60, End: 15 * 60}}))
	ta.mustOp(ta.Svc.SetFocus(t.Context(), ta.meta(), service.SetFocusIn{TaskID: focus}))
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Linked", TaskID: focus}))
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Standalone"}))
	entry := ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Deleted entry"})).Entry
	ta.mustOp(ta.Svc.DeleteEntry(t.Context(), ta.meta(), service.EntryRef{ID: entry.ID, Expect: domain.Expect{Gen: ta.Svc.Generation(), Version: entry.Version}}))
	return p
}

func taskPagePath(id domain.TaskID) string { return fmt.Sprintf("/ui/tasks/%d", id) }

func (p formPages) paths() []string {
	return []string{"/ui/", "/ui/triage", "/ui/backlog", "/ui/upcoming", "/ui/log", "/ui/log?include_deleted=1",
		"/ui/recap", "/ui/search?q=x", taskPagePath(p.focus), taskPagePath(p.done), taskPagePath(p.dropped), taskPagePath(p.deleted)}
}

func TestEveryFormKeepsHiddenFields(t *testing.T) {
	p := newFormPages(t)
	for _, route := range p.paths() {
		p.viewBody(route) // Checks every POST's hidden inputs, including entity ETags.
	}
}

func TestDangerFormsKeepConfirm(t *testing.T) {
	p := newFormPages(t)
	for _, route := range p.paths() {
		walkHTML(parseHTML(t, p.viewBody(route)), func(n *html.Node) {
			if n.Data != "form" {
				return
			}
			action := attr(n, "action")
			switch path.Base(action) {
			case "drop", "delete", "reopen":
				if strings.TrimSpace(attr(n, "hx-confirm")) == "" {
					t.Errorf("%s: %s lacks hx-confirm", route, action)
				}
			}
		})
	}
}

func TestPrimaryActionRows(t *testing.T) {
	p := newFormPages(t)
	for _, tc := range []struct {
		page          string
		id            domain.TaskID
		primary, more []string
	}{
		{"/ui/triage", p.triage, []string{"Keep", "Top", "Bottom", "Complete"}, []string{"Place before", "Place after", "Defer", "Block", "Drop"}},
		{"/ui/backlog", p.middle, []string{"Up", "Down", "Focus", "Complete"}, []string{"Top", "Bottom", "Send to triage", "Defer", "Block", "Drop"}},
		{"/ui/upcoming", p.deferred, []string{"Keep", "Change defer"}, nil},
		{"/ui/upcoming", p.due, []string{"Set due"}, nil},
		{taskPagePath(p.focus), p.focus, []string{"Complete", "Focus", "Keep"}, []string{"Top", "Bottom", "Place before", "Place after", "Defer", "Block", "Send to triage", "Drop", "Delete"}},
		{taskPagePath(p.done), p.done, []string{"Reopen"}, []string{"Delete"}},
		{taskPagePath(p.dropped), p.dropped, []string{"Reopen"}, []string{"Delete"}},
		{taskPagePath(p.deleted), p.deleted, []string{"Undelete"}, nil},
		{"/ui/", p.focus, []string{"Complete", "Clear focus"}, nil},
	} {
		t.Run(tc.page+"/"+fmt.Sprint(tc.id), func(t *testing.T) {
			tag := "article"
			if tc.page == "/ui/" {
				tag = "div"
			}
			card := requireElement(t, parseHTML(t, p.viewBody(tc.page)), tag, "data-task", fmt.Sprint(tc.id))
			var rows []*html.Node
			for child := range card.ChildNodes() {
				if child.Type == html.ElementNode && (attr(child, "class") == "actions" || child.Data == "details") {
					rows = append(rows, child)
				}
			}
			if len(rows) == 0 || rows[0].Data != "div" || attr(rows[0], "class") != "actions" {
				t.Fatal("card missing primary action row")
			}
			checkButtons(t, rows[0], tc.primary)
			if tc.more == nil {
				if len(rows) != 1 {
					t.Errorf("card has %d action groups, want one", len(rows))
				}
				return
			}
			if len(rows) != 2 || rows[1].Data != "details" || attr(rows[1], "class") != "more" || hasAttr(rows[1], "open") {
				t.Fatal("primary row must be followed by a closed More disclosure")
			}
			var summary string
			for child := range rows[1].ChildNodes() {
				if child.Data == "summary" {
					summary = nodeText(child)
				}
			}
			if summary != "More" {
				t.Errorf("disclosure summary = %q", summary)
			}
			checkButtons(t, requireElement(t, rows[1], "div", "class", "actions"), tc.more)
		})
	}
}

func checkButtons(t *testing.T, row *html.Node, want []string) {
	t.Helper()
	var got []string
	walkHTML(row, func(n *html.Node) {
		if n.Data != "button" {
			return
		}
		label := nodeText(n)
		got = append(got, label)
		class := "btn secondary"
		switch label {
		case "Complete", "Change defer", "Set due", "Reopen", "Undelete":
			class = "btn primary"
		case "Drop", "Delete":
			class = "btn danger"
		}
		if attr(n, "class") != class {
			t.Errorf("%s class = %q, want %q", label, attr(n, "class"), class)
		}
	})
	if !slices.Equal(got, want) {
		t.Errorf("buttons = %q, want %q", got, want)
	}
}

func TestFormDisclosures(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Draft"})
	for _, route := range []string{"/ui/", "/ui/triage", taskPagePath(id)} {
		action, summary := "/ui/tasks", "Placement"
		want := []string{"place", "ref", "defer", "span"}
		if route == taskPagePath(id) {
			action, summary = route+"/complete", "Complete with options"
			want = []string{"as", "date", "log", "notes"}
		}
		form := requireElement(t, parseHTML(t, ta.viewBody(route)), "form", "action", action)
		options := requireElement(t, form, "details", "class", "more")
		if hasAttr(options, "open") || nodeText(requireElement(t, options, "summary", "class", "")) != summary {
			t.Errorf("%s: want closed %q disclosure", route, summary)
		}
		var names []string
		walkHTML(options, func(n *html.Node) {
			if hasAttr(n, "name") {
				names = append(names, attr(n, "name"))
			}
		})
		if !slices.Equal(names, want) {
			t.Errorf("%s: fields inside disclosure = %v, want %v", route, names, want)
		}
		if button := requireElement(t, form, "button", "type", "submit"); button.Parent != form || attr(button, "class") != "btn primary" {
			t.Errorf("%s: expected primary submit outside disclosure", route)
		}
	}
	if len(scrapeForms(t, ta.viewBody("/ui/backlog"), "/ui/tasks")) != 0 {
		t.Error("add task form on Backlog")
	}
}

func TestDeferredAndDueChips(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Later", Due: ptrMoment(testutil.Moment("11-27 13:00")),
		Place: &service.AddPlacement{How: "defer", Defer: testutil.Moment("10-30 09:00")}})
	for _, route := range []string{"/ui/upcoming", taskPagePath(id)} {
		card := requireElement(t, parseHTML(t, ta.viewBody(route)), "article", "data-task", fmt.Sprint(id))
		for class, want := range map[string]string{"chip": "deferred until Fri 2026-10-30 09:00", "chip warn": "due Fri 2026-11-27 13:00"} {
			if got := nodeText(requireElement(t, card, "span", "class", class)); got != want {
				t.Errorf("%s: chip = %q, want %q", route, got, want)
			}
		}
	}
}

func TestBlockStatusChips(t *testing.T) {
	ta := newTestApp(t)
	tmpl, err := ta.App.Template("task")
	if err != nil {
		t.Fatal(err)
	}
	detail := ta.task(ta.addTask(service.AddTaskIn{Title: "Blocks"}))
	for status, class := range map[domain.BlockStatus]string{
		domain.BlockWorked: "chip ok", domain.BlockDone: "chip ok", domain.BlockMissed: "chip bad", domain.BlockCancelled: "chip muted",
	} {
		detail.Blocks = []domain.BlockView{{TimeBlock: domain.TimeBlock{ID: 1, Status: status}}}
		var body bytes.Buffer
		if err := tmpl.ExecuteTemplate(&body, "layout", web.Page{Data: detail}); err != nil {
			t.Fatal(err)
		}
		block := requireElement(t, parseHTML(t, body.String()), "article", "data-block", "1")
		if got := nodeText(requireElement(t, block, "span", "class", class)); got != string(status) {
			t.Errorf("%s: chip text = %q", status, got)
		}
	}
}
