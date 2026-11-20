package report

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/testutil"
)

var renderers = []struct {
	name   string
	render func(io.Writer, *api.Report) error
}{
	{"text", Text},
	{"markdown", Markdown},
}

func rpt01() *api.Report {
	id42, id7, id57 := int64(42), int64(7), int64(57)
	at := civil.Minute(11 * 60)
	return &api.Report{
		From: civil.MustDate("2026-10-23"), To: civil.MustDate("2026-10-26"),
		Days: []api.ReportDay{
			{Date: civil.MustDate("2026-10-23"), Lines: []api.ReportLine{
				{EntryID: 3, Kind: "work", Title: "Answered Bob's DNS question", Time: &at},
				{EntryID: 1, Kind: "work", Title: "Added Ansible role for postgres cluster", TaskID: &id42},
				{EntryID: 2, Kind: "work", Title: "Created pgk cluster in dev", TaskID: &id42},
			}},
			{Date: civil.MustDate("2026-10-26"), Lines: []api.ReportLine{
				{EntryID: 4, Kind: "work", Title: "Drafted outline", TaskID: &id7},
				{EntryID: 5, Kind: "work", Title: "Failover tested in dev", TaskID: &id42},
				{EntryID: 6, Kind: "complete", Title: "Set up keycloak postgres cluster", TaskID: &id42, Reopened: true},
				{EntryID: 7, Kind: "complete", Title: "Add svc-cluster weencrypt name to certs", TaskID: &id57},
			}},
		},
		Tasks: map[string]api.ReportTask{
			"42": {Title: "Set up keycloak postgres cluster", Status: "open"},
			"7":  {Title: "Write report", Status: "open"},
			"57": {Title: "Add svc-cluster weencrypt name to certs", Status: "done"},
		},
	}
}

func rendered(t *testing.T, render func(io.Writer, *api.Report) error, r *api.Report) string {
	t.Helper()
	var out bytes.Buffer
	if err := render(&out, r); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestRPT01_Text(t *testing.T) {
	testutil.Golden(t, "rpt01.text", []byte(rendered(t, Text, rpt01())))
}

func TestRPT09_Markdown(t *testing.T) {
	testutil.Golden(t, "rpt01.md", []byte(rendered(t, Markdown, rpt01())))
	r := rpt01()
	r.Days[0].Lines[0].Title = "a*b_c"
	if got := rendered(t, Markdown, r); !strings.Contains(got, "- a\\*b\\_c\n") {
		t.Fatalf("unescaped markdown title:\n%s", got)
	}
}

func TestRPT02_Markers(t *testing.T) {
	for _, renderer := range renderers {
		t.Run(renderer.name, func(t *testing.T) {
			got := rendered(t, renderer.render, rpt01())
			if strings.Count(got, "✓") != 2 || strings.Count(got, "(reopened)") != 1 {
				t.Fatalf("wrong completion/reopened markers:\n%s", got)
			}
			for _, line := range strings.Split(got, "\n") {
				if strings.Contains(line, "✓") && !strings.Contains(line, "✓ Set up keycloak postgres cluster") &&
					!strings.Contains(line, "✓ Add svc-cluster weencrypt name to certs") {
					t.Errorf("unexpected completion marker: %q", line)
				}
				if strings.Contains(line, "(reopened)") &&
					(!strings.Contains(line, "✓ Set up keycloak postgres cluster") || !strings.HasSuffix(line, " (reopened)")) {
					t.Errorf("unexpected reopened suffix: %q", line)
				}
			}
		})
	}
}

func TestRPT03_FirstAppearance(t *testing.T) {
	r := rpt01()
	r.From = civil.MustDate("2026-10-26")
	r.Days = r.Days[1:]
	for _, tt := range []struct {
		name   string
		render func(io.Writer, *api.Report) error
		want   string
	}{
		{"text", Text, "  - Failover tested in dev                   [#42 Set up keycloak postgres cluster]\n"},
		{"markdown", Markdown, "- Failover tested in dev (#42 Set up keycloak postgres cluster)\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Repeated calls must each start a fresh first-appearance set.
			for range 2 {
				if got := rendered(t, tt.render, r); !strings.Contains(got, tt.want) {
					t.Fatalf("missing first annotation %q:\n%s", tt.want, got)
				}
			}
		})
	}
}

func TestRPT04_Standalone(t *testing.T) {
	for _, renderer := range renderers {
		t.Run(renderer.name, func(t *testing.T) {
			got := rendered(t, renderer.render, rpt01())
			for _, line := range strings.Split(got, "\n") {
				if strings.Contains(line, "Answered Bob's DNS question") {
					if strings.ContainsAny(line, "[(") || strings.TrimRight(line, " ") != line {
						t.Fatalf("standalone annotation or trailing padding: %q", line)
					}
					return
				}
			}
			t.Fatal("standalone line missing")
		})
	}
}

func withTaskSections(r *api.Report) *api.Report {
	at := civil.Minute(9 * 60)
	r.Todo = &api.ReportTodo{
		Triage:  []api.Task{{ID: 3, Title: "Pay invoice", Reasons: []api.Reason{{Reason: "overdue"}, {Reason: "new"}}}},
		Backlog: []api.Task{{ID: 4, Title: "Write notes", Reasons: []api.Reason{{Reason: "new"}}}},
	}
	r.Upcoming = []api.Task{
		{ID: 5, Title: "Renew cert", Deferred: true, Defer: &api.Moment{Date: civil.MustDate("2026-10-30"), Time: &at}},
		{ID: 6, Title: "Send invoice", Due: &api.Moment{Date: civil.MustDate("2026-10-31")}},
	}
	return r
}

func TestRPT05_TodoUpcoming(t *testing.T) {
	for _, tt := range []struct {
		name   string
		render func(io.Writer, *api.Report) error
		want   string
	}{
		{"text", Text, "\nTriage\n  - #3 Pay invoice [overdue, new]\n\nBacklog\n  - #4 Write notes\n\nUpcoming\n  - #5 Renew cert (deferred to 2026-10-30 09:00)\n  - #6 Send invoice (due 2026-10-31)\n"},
		{"markdown", Markdown, "\n## Triage\n\n- #3 Pay invoice [overdue, new]\n\n## Backlog\n\n- #4 Write notes\n\n## Upcoming\n\n- #5 Renew cert (deferred to 2026-10-30 09:00)\n- #6 Send invoice (due 2026-10-31)\n"},
	} {
		for _, emptyDays := range []bool{false, true} {
			t.Run(tt.name+map[bool]string{false: "/days", true: "/empty_days"}[emptyDays], func(t *testing.T) {
				r := withTaskSections(rpt01())
				if emptyDays {
					r.Days = nil // BF20: optional sections survive an empty work range.
				}
				got := rendered(t, tt.render, r)
				if !strings.HasSuffix(got, tt.want) {
					t.Fatalf("task sections = %q, want suffix %q", got, tt.want)
				}
				if emptyDays {
					header := "Work report Fri 2026-10-23 – Mon 2026-10-26\n"
					if tt.name == "markdown" {
						header = "# " + header
					} else {
						header += "\nNo work logged.\n"
					}
					if got != header+tt.want {
						t.Fatalf("empty range with sections = %q, want %q", got, header+tt.want)
					}
				}
			})
		}
	}
}

func TestTaskSectionsIndependent(t *testing.T) {
	for _, renderer := range renderers {
		for _, section := range []string{"todo", "upcoming", "empty_todo"} {
			t.Run(renderer.name+"/"+section, func(t *testing.T) {
				r := withTaskSections(rpt01())
				r.Days = nil
				if section == "upcoming" {
					r.Todo = nil
				} else {
					r.Upcoming = nil
					if section == "empty_todo" {
						r.Todo = &api.ReportTodo{}
					}
				}
				got := rendered(t, renderer.render, r)
				if strings.Contains(got, "Triage") != (section != "upcoming") ||
					strings.Contains(got, "Backlog") != (section != "upcoming") ||
					strings.Contains(got, "Upcoming") != (section == "upcoming") {
					t.Fatalf("incorrect optional sections:\n%s", got)
				}
			})
		}
	}
}

func TestUpcomingMoments(t *testing.T) {
	at := civil.Minute(9 * 60)
	r := rpt01()
	r.Days = nil
	r.Upcoming = []api.Task{
		{ID: 5, Title: "Renew cert", Deferred: true, Defer: &api.Moment{Date: civil.MustDate("2026-10-30")}, Due: &api.Moment{Date: civil.MustDate("2026-10-31")}},
		{ID: 6, Title: "Send invoice", Defer: &api.Moment{Date: civil.MustDate("2026-10-20")}, Due: &api.Moment{Date: civil.MustDate("2026-10-31"), Time: &at}},
	}
	for _, renderer := range renderers {
		t.Run(renderer.name, func(t *testing.T) {
			got := rendered(t, renderer.render, r)
			for _, want := range []string{"#5 Renew cert (deferred to 2026-10-30)\n", "#6 Send invoice (due 2026-10-31 09:00)\n"} {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestLongTitleTruncated(t *testing.T) {
	r := rpt01()
	long := strings.Repeat("a", 70)
	r.Days[0].Lines[0].Title = long
	r.Days[0].Lines[1].Title = long
	got := rendered(t, Text, r)
	want := strings.Repeat("a", 59) + "…"
	for _, line := range []string{"  - " + want + "\n", "  - " + want + "  [#42 Set up keycloak postgres cluster]\n"} {
		if !strings.Contains(got, line) {
			t.Errorf("missing truncated line %q:\n%s", line, got)
		}
	}
	if md := rendered(t, Markdown, r); !strings.Contains(md, "- "+long+"\n") {
		t.Fatalf("markdown title unexpectedly truncated:\n%s", md)
	}
}

func TestUnicodeTitleWidth(t *testing.T) {
	r := rpt01()
	r.Days = r.Days[:1]
	r.Days[0].Lines = r.Days[0].Lines[1:]
	r.Days[0].Lines[0].Title = "界e\u0301"
	r.Days[0].Lines[1].Title = "a"
	got := rendered(t, Text, r)
	want := "  - 界e\u0301  [#42 Set up keycloak postgres cluster]\n  - a    [#42]\n"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("terminal column alignment = %q, want suffix %q", got, want)
	}
	r.Days[0].Lines[0].Title = strings.Repeat("界", 29) + "e\u0301" + strings.Repeat("界", 5)
	got = rendered(t, Text, r)
	want = "  - " + strings.Repeat("界", 29) + "e\u0301…  [#42 Set up keycloak postgres cluster]\n"
	if !strings.Contains(got, want) {
		t.Fatalf("grapheme truncation = %q, want line %q", got, want)
	}
}

func TestEmptyReport(t *testing.T) {
	for _, renderer := range renderers {
		t.Run(renderer.name, func(t *testing.T) {
			r := rpt01()
			r.Days = []api.ReportDay{}
			want := "Work report Fri 2026-10-23 – Mon 2026-10-26\n"
			if renderer.name == "markdown" {
				want = "# " + want
			} else {
				want += "\nNo work logged.\n"
			}
			if got := rendered(t, renderer.render, r); got != want {
				t.Fatalf("empty report = %q, want %q", got, want)
			}
		})
	}
}

func TestReportFilters(t *testing.T) {
	r := rpt01()
	r.Days = nil
	tag, project := "infra", "infra.keycloak"
	r.Tag, r.Project = &tag, &project
	for _, renderer := range renderers {
		t.Run(renderer.name, func(t *testing.T) {
			got := rendered(t, renderer.render, r)
			want := "Work report Fri 2026-10-23 – Mon 2026-10-26 · tag infra · project infra.keycloak\n"
			if renderer.name == "markdown" {
				want = "# " + want
			} else {
				want += "\nNo work logged.\n"
			}
			if got != want {
				t.Fatalf("filtered header = %q, want %q", got, want)
			}
		})
	}
}

func TestSanitized(t *testing.T) {
	r := withTaskSections(rpt01())
	dirty, clean := "x\x1b[2Jy\n\t\r\x00\u202e\xff", "x�[2Jy������"
	r.Tag, r.Project = &dirty, &dirty
	r.Days[0].Lines[0].Title = dirty
	r.Tasks["42"] = api.ReportTask{Title: dirty}
	r.Todo.Triage[0].Title = dirty
	r.Todo.Triage[0].Reasons = []api.Reason{{Reason: dirty}}
	r.Todo.Backlog[0].Title = dirty
	r.Upcoming[0].Title = dirty
	for _, renderer := range renderers {
		t.Run(renderer.name, func(t *testing.T) {
			got := rendered(t, renderer.render, r)
			want := clean
			if renderer.name == "markdown" {
				want = strings.ReplaceAll(want, "[", "\\[")
			}
			if !utf8.ValidString(got) || strings.ContainsAny(got, "\x1b\t\r\x00\u202e") || strings.Count(got, want) != 8 {
				t.Fatalf("unsafe or missing user text, want 8 instances of %q:\n%q", want, got)
			}
		})
	}
}

func TestMarkdownEscapes(t *testing.T) {
	r := withTaskSections(rpt01())
	text := "\\`*_[]<>#|~"
	want := "\\\\\\`\\*\\_\\[\\]\\<\\>\\#\\|\\~"
	r.Tag, r.Project = &text, &text
	r.Days[0].Lines[0].Title = text
	r.Tasks["42"] = api.ReportTask{Title: text}
	r.Todo.Triage[0].Title = text
	r.Todo.Triage[0].Reasons = []api.Reason{{Reason: text}}
	r.Todo.Backlog[0].Title = text
	r.Upcoming[0].Title = text
	if got := rendered(t, Markdown, r); strings.Count(got, want) != 8 {
		t.Fatalf("expected every user-text field escaped (%q):\n%s", want, got)
	}
}

func TestMarkdownListPrefixes(t *testing.T) {
	for _, tt := range []struct {
		title, want string
	}{
		{"- Review DNS", `\- Review DNS`},
		{"+ Review DNS", `\+ Review DNS`},
		{"* Review DNS", `\* Review DNS`},
		{"1. Review DNS", `1\. Review DNS`},
		{"1) Review DNS", `1\) Review DNS`},
		{"123456789. Review DNS", `123456789\. Review DNS`},
		{"0) Review DNS", `0\) Review DNS`},
		{"   - Review DNS", `   \- Review DNS`},
		{"   1. Review DNS", `   1\. Review DNS`},
		{"-", `\-`},
		{"+", `\+`},
		{"1.", `1\.`},
		{"1)", `1\)`},
		{"svc-cluster (v1.2) + DNS", "svc-cluster (v1.2) + DNS"},
	} {
		t.Run(tt.title, func(t *testing.T) {
			for _, linked := range []bool{false, true} {
				r := rpt01()
				r.Days = r.Days[:1]
				r.Days[0].Lines = []api.ReportLine{{Kind: "work", Title: tt.title}}
				want := "# Work report Fri 2026-10-23 – Mon 2026-10-26\n\n## Fri 2026-10-23\n\n- " + tt.want
				if linked {
					id := int64(42)
					r.Days[0].Lines[0].TaskID = &id
					want += " (#42 Set up keycloak postgres cluster)"
				}
				want += "\n"
				if got := rendered(t, Markdown, r); got != want {
					t.Errorf("linked=%v: Markdown = %q, want %q", linked, got, want)
				}
			}
		})
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriterErrors(t *testing.T) {
	want := errors.New("output unavailable")
	for _, renderer := range renderers {
		t.Run(renderer.name, func(t *testing.T) {
			if err := renderer.render(failingWriter{err: want}, withTaskSections(rpt01())); !errors.Is(err, want) {
				t.Fatalf("writer error = %v, want %v", err, want)
			}
		})
	}
}
