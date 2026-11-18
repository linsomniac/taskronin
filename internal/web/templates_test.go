package web_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/testutil"
	"taskronin/internal/web"
)

func TestTemplatesLint(t *testing.T) {
	err := fs.WalkDir(web.TemplateFS, "templates", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		data, err := web.TemplateFS.ReadFile(path)
		if err != nil {
			return err
		}
		for _, problem := range lintTemplate(path, string(data)) {
			t.Error(problem)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func lintTemplate(path, source string) []string {
	var problems []string
	script := `<script src="/static/htmx-2.0.11.min.js"></script>`
	forms := regexp.MustCompile(`(?i)<form\b[^>]*\bmethod\s*=\s*["']?post`)
	formEnd := regexp.MustCompile(`(?i)</form\s*>`)
	events := regexp.MustCompile(`(?i)[\s/"']on[a-z]+\s*=`)
	styles := regexp.MustCompile(`(?i)[\s/"']style\s*=`)
	lower := strings.ToLower(source)
	for _, forbidden := range []string{"<style", "hx-on", "javascript:"} {
		if strings.Contains(lower, forbidden) {
			problems = append(problems, fmt.Sprintf("%s contains %q", path, forbidden))
		}
	}
	if events.MatchString(source) {
		problems = append(problems, fmt.Sprintf("%s contains an event attribute", path))
	}
	if styles.MatchString(source) {
		problems = append(problems, fmt.Sprintf("%s contains an inline style", path))
	}
	withoutScript := source
	if path == "templates/layout.html" {
		if strings.Count(source, script) != 1 {
			problems = append(problems, "layout must load exactly one pinned htmx script")
		}
		withoutScript = strings.ReplaceAll(source, script, "")
		for _, required := range []string{
			`<meta name="htmx-config" content='{"allowEval":false,"allowScriptTags":false,"selfRequestsOnly":true,"includeIndicatorStyles":false,"historyCacheSize":0,"refreshOnHistoryMiss":true}'>`,
			`hx-boost="true"`, `hx-headers='{"X-CSRF-Token":"{{.CSRF}}"}'`, `href="/static/app.css?v={{.Version}}"`,
		} {
			if !strings.Contains(source, required) {
				problems = append(problems, fmt.Sprintf("layout lacks %s", required))
			}
		}
	}
	if strings.Contains(strings.ToLower(withoutScript), "<script") {
		problems = append(problems, fmt.Sprintf("%s has an unauthorized script", path))
	}
	for _, match := range forms.FindAllStringIndex(source, -1) {
		form := source[match[0]:]
		end := formEnd.FindStringIndex(form)
		if end == nil {
			problems = append(problems, fmt.Sprintf("%s has an unclosed POST form", path))
			continue
		}
		form = form[:end[1]]
		if path == "templates/login.html" {
			continue
		}
		if strings.Contains(form, `{{template "hidden" $}}`) || (path == "templates/partials.html" && strings.Contains(form, `{{template "hidden" .P}}`)) {
			continue
		}
		if path == "templates/error.html" && strings.Contains(form, `{{template "csrfnonce" $}}`) {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s form lacks the required hidden partial: %s", path, form))
	}
	return problems
}

func TestTemplateHelpersAndRowContext(t *testing.T) {
	ta := newTestApp(t)
	tmpl, err := ta.App.Template("error")
	if err != nil {
		t.Fatal(err)
	}
	_, err = tmpl.Parse(`{{define "probeRow"}}<form method="post" action="/ui/probe">
{{template "hidden" .P}}<input name="etag" value="{{etag .P.Gen .V.Version}}"><input name="title" value="{{.V.Title}}"></form>{{end}}
{{define "content"}}{{template "probeRow" (row $ .Data)}}
<p id="day">{{day .Today}}</p><p id="start">{{hhmm .Data.Start}}</p><p id="end">{{hhmm .Data.End}}</p>
<p id="wall">{{wall .Data.When}}</p><p id="short">{{short .Data.OpID}}</p>
<p id="json">{{jsontext .Data.JSON}}</p><p id="niljson">{{jsontext .Data.NilJSON}}</p>
<div id="markdown">{{md $.MD .Data.Notes}}</div>{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	data := struct {
		Version       domain.Version
		Title         string
		Start         civil.Minute
		End           civil.EndMinute
		When          time.Time
		OpID          domain.OpID
		JSON, NilJSON json.RawMessage
		Notes         string
	}{Version: 3, Title: `<script>row title</script>`, Start: 545, End: 1440,
		When: testutil.Local(t, "2026-10-28 10:00"), OpID: domain.OpID{0xab, 0xcd, 0xef, 0x12},
		JSON: json.RawMessage(`{"value":"<script>"}`), Notes: "**bold**"}
	r := httptest.NewRequestWithContext(web.WithSession(t.Context(), web.SessionInfo{CSRF: "row-token"}), http.MethodGet, "/ui/log", nil)
	p := ta.App.NewPage(r, "Test", "log", data)
	var body bytes.Buffer
	if err := tmpl.ExecuteTemplate(&body, "layout", p); err != nil {
		t.Fatal(err)
	}
	form := scrapeForm(t, body.String(), "/ui/probe")
	if form.Get("_csrf") != p.CSRF || form.Get("_gen") != string(p.Gen) || form.Get("_return") != p.Return || form.Get("title") != data.Title || form.Get("etag") != domain.FormatETag(p.Gen, data.Version) {
		t.Errorf("row context = %v", form)
	}
	values := make(map[string]string)
	walkHTML(parseHTML(t, body.String()), func(n *html.Node) {
		if id := attr(n, "id"); id != "" {
			values[id] = nodeText(n)
		}
	})
	for id, want := range map[string]string{"day": "Wed 2026-10-28", "start": "09:05", "end": "24:00", "wall": "Wed 2026-10-28 10:00", "short": "abcdef12", "json": string(data.JSON), "niljson": "", "markdown": "bold\n"} {
		if values[id] != want {
			t.Errorf("%s = %q, want %q", id, values[id], want)
		}
	}
	logout := scrapeForm(t, body.String(), "/ui/logout")
	if form.Get("_nonce") == "" || form.Get("_nonce") == logout.Get("_nonce") {
		t.Error("row and logout forms did not get independent nonces")
	}
	assertInert(t, body.String())
}

func TestTemplatesLintBrowserSyntax(t *testing.T) {
	for _, source := range []string{
		`<svg/onload="alert(1)"></svg>`, `<a href="x"onclick="alert(1)">x</a>`,
		`<p style = "color:red">x</p>`, `<p title='x'style='color:red'>x</p>`,
		`<form method="POST"></form>`, `<FORM method=post></FORM>`, `<form method = 'post'></form>`,
		`<form method="post">{{template "hidden" $}}</form><form method=post></form>`,
	} {
		t.Run(source, func(t *testing.T) {
			if problems := lintTemplate("templates/probe.html", source); len(problems) == 0 {
				t.Error("lint accepted unsafe template syntax or a POST without hidden fields")
			}
		})
	}
	for _, source := range []string{
		`<form method="POST">{{template "hidden" $}}</form>`,
		`<FORM method=post>{{template "hidden" $}}</FORM>`,
		`<form method = 'post'>{{template "hidden" $}}</form>`,
	} {
		if problems := lintTemplate("templates/probe.html", source); len(problems) != 0 {
			t.Errorf("valid form rejected: %v", problems)
		}
	}
}

func TestHHMMRejectsUnsupportedTypes(t *testing.T) {
	ta := newTestApp(t)
	tmpl, err := ta.App.Template("error")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmpl.Parse(`{{define "probe"}}{{hhmm .}}{{end}}`); err != nil {
		t.Fatal(err)
	}
	minute := civil.Minute(545)
	for _, value := range []any{545, "09:05", &minute, sql.Null[civil.Minute]{V: minute, Valid: true}, nil} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			var body bytes.Buffer
			if err := tmpl.ExecuteTemplate(&body, "probe", value); err == nil {
				t.Error("hhmm silently accepted an unsupported type")
			}
		})
	}
}
