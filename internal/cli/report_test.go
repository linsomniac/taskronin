package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func TestDAT13_Ranges(t *testing.T) {
	t.Run("monday", func(t *testing.T) {
		_, sys := captureServer(t)
		for _, tc := range []struct {
			name, from, to string
			flags          []string
		}{
			{"default", "2026-10-26", "2026-11-01", nil},
			{"week", "2026-10-26", "2026-11-01", []string{"--week"}},
			{"last week", "2026-10-19", "2026-10-25", []string{"--last-week"}},
			{"month", "2026-10-01", "2026-10-31", []string{"--month"}},
			{"today", "2026-10-28", "2026-10-28", []string{"--today"}},
			{"explicit", "2026-10-23", "2026-10-28", []string{"--from", "2026-10-23", "--to", "2026-10-28"}},
			{"past weekdays", "2026-10-23", "2026-10-28", []string{"--from", "fri", "--to", "wed"}},
			{"relative", "2026-10-27", "2026-10-28", []string{"--from", "yesterday", "--to", "today"}},
			{"false selector", "2026-10-28", "2026-10-28", []string{"--week=false", "--today"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				out := captureRun(t, sys, "", append([]string{"report", "--format", "json"}, tc.flags...)...)
				var report api.Report
				if err := json.Unmarshal([]byte(out), &report); err != nil {
					t.Fatal(err)
				}
				if report.From.String() != tc.from || report.To.String() != tc.to {
					t.Fatalf("range=%s..%s want=%s..%s", report.From, report.To, tc.from, tc.to)
				}
			})
		}
		out, stderr, code := runCLI(t, sys, "", "report", "--from", "2026-10-30", "--to", "2026-10-28")
		if code != 2 || out != "" || !strings.Contains(stderr, "--from must not be after --to") {
			t.Fatalf("reversed range: exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("sunday", func(t *testing.T) {
		testutil.StartServer(t, testutil.WithClock("2026-10-28 10:00"), testutil.WithConfig(func(c *config.Server) {
			c.WeekStart = "sunday"
		}))
		out := captureRun(t, sysDeps{Getenv: os.Getenv}, "", "report", "--week", "--format", "json")
		var report api.Report
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatal(err)
		}
		if report.From.String() != "2026-10-25" || report.To.String() != "2026-10-31" {
			t.Fatalf("sunday week=%s..%s", report.From, report.To)
		}
	})
}

func TestReportEndToEnd(t *testing.T) {
	_, sys := captureServer(t)
	captureRun(t, sys, "", "add", "Set up keycloak")
	captureRun(t, sys, "", "log", "1", "Added Ansible role", "-m", "Entry notes")
	want := "Work report Wed 2026-10-28 – Wed 2026-10-28\n\nWed 2026-10-28\n  - Added Ansible role  [#1 Set up keycloak]\n"
	for _, flags := range [][]string{nil, {"--format", "text"}} {
		if out := captureRun(t, sys, "", append([]string{"report", "--today"}, flags...)...); out != want {
			t.Fatalf("stdout=%q want=%q", out, want)
		}
	}
	for _, flags := range [][]string{{"--format", "json"}, {"--json"}} {
		out := captureRun(t, sys, "", append([]string{"report", "--today"}, flags...)...)
		var report api.Report
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out, "{\n  \"from\":") || len(report.Days) != 1 || len(report.Days[0].Lines) != 1 || report.Tasks["1"].Title != "Set up keycloak" {
			t.Fatalf("indented report JSON=%q", out)
		}
		line := report.Days[0].Lines[0]
		if line.Title != "Added Ansible role" || line.Notes != "Entry notes" || line.TaskID == nil || *line.TaskID != 1 {
			t.Fatalf("report line=%+v", line)
		}
	}
	out := captureRun(t, sys, "", "report", "--today", "--format", "md")
	want = "# Work report Wed 2026-10-28 – Wed 2026-10-28\n\n## Wed 2026-10-28\n\n- Added Ansible role (#1 Set up keycloak)\n"
	if out != want {
		t.Fatalf("Markdown=%q want=%q", out, want)
	}
}

func TestRPT10_EmptyRange(t *testing.T) {
	_, sys := captureServer(t)
	out := captureRun(t, sys, "", "report", "--last-week", "--color", "always")
	want := "Work report Mon 2026-10-19 – Sun 2026-10-25\n\nNo work logged.\n"
	if out != want {
		t.Fatalf("stdout=%q want=%q", out, want)
	}
	out = captureRun(t, sys, "", "report", "--last-week", "--format", "json")
	var report api.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Days == nil || len(report.Days) != 0 || report.Tasks == nil || len(report.Tasks) != 0 {
		t.Fatalf("empty JSON=%q err=%v", out, err)
	}
	captureRun(t, sys, "", "add", "Triage task")
	captureRun(t, sys, "", "add", "Backlog task", "--bottom")
	captureRun(t, sys, "", "add", "Deferred task", "--defer", "tomorrow")
	for _, format := range []string{"text", "md", "json"} {
		out = captureRun(t, sys, "", "report", "--last-week", "--todo", "--upcoming", "--format", format)
		if format == "json" {
			if err := json.Unmarshal([]byte(out), &report); err != nil || report.Todo == nil || len(report.Todo.Triage) != 1 || len(report.Todo.Backlog) != 1 || len(report.Upcoming) != 1 {
				t.Fatalf("optional sections JSON=%q err=%v", out, err)
			}
			continue
		}
		triage, backlog, upcoming := strings.Index(out, "Triage\n"), strings.Index(out, "Backlog\n"), strings.Index(out, "Upcoming\n")
		if triage < 0 || backlog <= triage || upcoming <= backlog {
			t.Fatalf("optional section order: %q", out)
		}
		for _, title := range []string{"#1 Triage task", "#2 Backlog task", "#3 Deferred task"} {
			if !strings.Contains(out, title) {
				t.Errorf("missing %q in %q", title, out)
			}
		}
	}
}

func TestRPT06_Filters(t *testing.T) {
	_, sys := captureServer(t)
	captureRun(t, sys, "", "add", "Keycloak", "-t", "infra", "-P", "infra.keycloak")
	captureRun(t, sys, "", "add", "Infrastructure", "-P", "infrastructure")
	captureRun(t, sys, "", "log", "1", "Keycloak work")
	captureRun(t, sys, "", "log", "2", "Infrastructure work")
	captureRun(t, sys, "", "log", "-N", "Standalone work")
	for _, flags := range [][]string{{"-t", "Infra"}, {"-P", "infra"}, {"-t", "infra", "-P", "infra"}} {
		out := captureRun(t, sys, "", append([]string{"report", "--today"}, flags...)...)
		if !strings.Contains(out, "Keycloak work  [#1 Keycloak]") || strings.Contains(out, "Infrastructure work") || strings.Contains(out, "Standalone work") {
			t.Fatalf("flags=%v stdout=%q", flags, out)
		}
	}
}

func TestReportInvalidArgs(t *testing.T) {
	_, sys := captureServer(t)
	for _, flags := range [][]string{
		{"extra"}, {"--format", "yaml"}, {"--week", "--today"}, {"--last-week", "--month"},
		{"--today", "--from", "yesterday", "--to", "today"},
		{"--from", "today"}, {"--to", "today"}, {"--from=", "--to=today"}, {"--from=today", "--to="},
		{"--from", "next friday", "--to", "today"}, {"--from", "today", "--to", "2026-02-30"},
		{"--from", "today 09:00", "--to", "today"}, {"--from", "today", "--to", "12:00"},
	} {
		out, stderr, code := runCLI(t, sys, "", append([]string{"report"}, flags...)...)
		if code != 2 || out != "" || stderr == "" {
			t.Errorf("flags=%v exit=%d stdout=%q stderr=%q", flags, code, out, stderr)
		}
	}
}
