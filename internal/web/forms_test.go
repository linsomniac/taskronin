package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"taskronin/internal/datespec"
	"taskronin/internal/domain"
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
	for _, value := range []string{"/ui/", "/ui/backlog", "/ui/log?from=2026-10-20&to=2026-10-28", "/ui/tasks/42#entries"} {
		if got := web.SafeReturn(value); got != value {
			t.Errorf("SafeReturn(%q) = %q", value, got)
		}
	}
	for _, value := range []string{"", "https://evil.example/", "//evil.example/ui/", `/\evil.example`, "javascript:alert(1)",
		"/api/v1/tasks", "/ui/../api/v1/tasks", "/static/app.css", "/ui/\r\nSet-Cookie: x=1", "ui/backlog",
		"/ui/%2e%2e/api/v1/tasks", "/ui/%5cevil", "/ui/%0d%0aX:1"} {
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
