package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/config"
	"taskronin/internal/domain"
)

func TestExitCodes(t *testing.T) {
	groups := map[int][]domain.Code{
		1: {domain.CodeValidation, domain.CodeIdemMismatch, domain.CodeMalformed, domain.CodeIdemRequired, domain.CodeTooLarge, domain.CodeUnsupportedMedia, domain.CodeInternal},
		4: {domain.CodeUnauthenticated, domain.CodeForbidden, domain.CodePeerRejected, domain.CodePasswordRequired, domain.CodePasswordInvalid},
		5: {domain.CodeNotFound},
		6: {domain.CodeConflict, domain.CodeDeleted, domain.CodeRevertConflict, domain.CodeNotRevertible, domain.CodeNothingToUndo, domain.CodeNothingToRedo, domain.CodePrecondition, domain.CodePreconditionReq, domain.CodeGenerationMismatch},
		7: {domain.CodeRateLimited, domain.CodeBusy},
	}
	for want, codes := range groups {
		for _, code := range codes {
			for _, err := range []error{domain.Errorf(code, "detail"), &client.ProblemError{Problem: api.Problem{Code: string(code)}}} {
				if got := exitCode(fmt.Errorf("wrapped: %w", err)); got != want {
					t.Errorf("%s = %d, want %d", code, got, want)
				}
			}
		}
	}
	for _, tc := range []struct {
		err  error
		want int
	}{
		{nil, 0}, {client.ErrNotSent, 3}, {client.ErrOutcomeUnknown, 3}, {&client.OutcomeUnknownError{Err: io.EOF}, 3},
		{client.ErrTokenFile, 4}, {usagef("bad %s", "argument"), 2}, {context.Canceled, 130}, {context.DeadlineExceeded, 3},
		{&client.OutcomeUnknownError{Err: context.Canceled}, 130}, {errors.New("plain"), 1},
		{&checkFoundError{detail: "found"}, 8}, {fmt.Errorf("wrapped: %w", &checkFoundError{detail: "found"}), 8},
	} {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("%v = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestQuoteArgs(t *testing.T) {
	got := quoteArgs([]string{"add", "it's", "a b", "--now", "x\x1b[2Jy", "plain/path-1.2"})
	want := "add 'it'\\''s' 'a b' --now 'x�[2Jy' plain/path-1.2"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := quoteArgs([]string{"", "$(touch x)", "a\nb"}); got != "'' '$(touch x)' 'a�b'" {
		t.Fatal(got)
	}
}

func TestRenderProblem(t *testing.T) {
	s, _ := testSys(t)
	p := api.Problem{Type: "about:blank", Title: "Unprocessable Entity", Status: 422, Code: "validation", Detail: "invalid task", Errors: []api.FieldError{{Field: "title", Message: "too long"}}}
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(p.Status); writeJSON(t, w, p) }))
	args := []string{"--socket", socket, "--token-file", token, "__test-clock"}
	out, stderr, code := runCLI(t, s, "", args...)
	if code != 1 || out != "" || stderr != "tkr: invalid task\n  title: too long\n" {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
	out, stderr, code = runCLI(t, s, "", append(args, "--json")...)
	var got api.Problem
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if code != 1 || stderr != "" || got.Detail != p.Detail || len(got.Errors) != 1 {
		t.Fatalf("%d %q %#v", code, stderr, got)
	}
}

func TestProblemConflictAndCurrent(t *testing.T) {
	s, _ := testSys(t)
	at, err := time.Parse(time.RFC3339, "2026-10-28T09:15:00Z")
	if err != nil {
		t.Fatal(err)
	}
	p := api.Problem{Status: 412, Code: "precondition_failed", Detail: "changed\x1b[2J", Current: json.RawMessage(`{"id":42,"version":3}`), Conflicts: []api.Conflict{{OpID: "abcdef0123456789", Kind: "task.update", Actor: "owner", At: api.Time{Time: at}, Entity: "task", EntityID: 42}}}
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(p.Status); writeJSON(t, w, p) }))
	_, stderr, code := runCLI(t, s, "", "--socket", socket, "--token-file", token, "__test-clock")
	want := "tkr: changed�[2J\n  op abcdef01 task.update by owner at 2026-10-28 09:15 (task 42)\n  current: #42 version 3\n"
	if code != 6 || stderr != want {
		t.Fatalf("%d %q", code, stderr)
	}
}

func TestOutcomeUnknownMessages(t *testing.T) {
	for _, kind := range []string{"saved", "read-only", "exempt"} {
		t.Run(kind, func(t *testing.T) {
			s, p := testSys(t)
			if kind == "read-only" {
				if err := os.MkdirAll(p.PendingDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(p.PendingDir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(p.PendingDir, 0o700) })
			}
			socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}))
			verb := "add"
			if kind == "exempt" {
				verb = "settings"
			}
			args := []string{"--socket", socket, "--token-file", token, "__test-mutate", verb, "--draft", "/draft path"}
			out, stderr, code := runCLI(t, s, "", args...)
			if code != 3 || out != "" || strings.Contains(stderr, "nothing was saved") {
				t.Fatalf("%d %q %q", code, out, stderr)
			}
			if kind == "saved" {
				if !strings.Contains(stderr, "saved as pending request") || !strings.Contains(stderr, "run `tkr pending retry`") {
					t.Fatal(stderr)
				}
				pending, err := (&client.PendingStore{Dir: p.PendingDir}).List()
				if err != nil || len(pending) != 1 {
					t.Fatalf("pending = %v, %v", pending, err)
				}
			} else if !strings.Contains(stderr, "was not saved for retry") || !strings.Contains(stderr, "check with `tkr show` / `tkr entries`") || !strings.Contains(stderr, "  tkr "+quoteArgs(args)+"\n") {
				t.Fatal(stderr)
			}
			if !strings.Contains(stderr, "tkr: your draft is at /draft path\n") {
				t.Fatal(stderr)
			}
		})
	}
}

func TestNotSentAndCanceledRendering(t *testing.T) {
	s, p := testSys(t)
	var out, stderr bytes.Buffer
	e := &env{sys: s, paths: p, out: &out, err: &stderr, argv: []string{"add", "a b"}, socket: "/socket", mutates: true, draftPath: "/draft"}
	err := fmt.Errorf("%w: %w", client.ErrNotSent, &net.OpError{Op: "dial", Net: "unix", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}})
	if code := e.fail(err); code != 3 {
		t.Fatal(code)
	}
	want := "tkr: cannot reach the Taskronin server at /socket: connection refused\ntkr: nothing was saved. Your command was:\n  tkr add 'a b'\ntkr: start it with: systemctl --user start taskronin.socket\ntkr: your draft is at /draft\n"
	if stderr.String() != want {
		t.Fatalf("%q", stderr.String())
	}
	stderr.Reset()
	if code := e.fail(&client.OutcomeUnknownError{Saved: true, Key: "request-key", Err: context.Canceled}); code != 130 || !strings.Contains(stderr.String(), "saved as pending request") {
		t.Fatalf("%d %q", code, stderr.String())
	}
}

func TestTokenFileMutationEcho(t *testing.T) {
	s, p := testSys(t)
	_, stderr, code := runCLI(t, s, "", "__test-mutate", "add")
	for _, part := range []string{p.OwnerToken, "run `tkr init`", "--token-file", "nothing was saved. Your command was:", "  tkr __test-mutate add\n"} {
		if !strings.Contains(stderr, part) {
			t.Errorf("missing %q in %q", part, stderr)
		}
	}
	if code != 4 {
		t.Fatal(code)
	}
}

func TestUnusableTokenFiles(t *testing.T) {
	for _, source := range []string{"default", "flag", "config"} {
		for _, kind := range []string{"missing", "mode", "symlink", "empty", "directory", "crlf"} {
			t.Run(source+"/"+kind, func(t *testing.T) {
				s, p := testSys(t)
				socket, validToken := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(t, w, map[string]any{})
				}))
				token := p.OwnerToken
				args := []string{"--socket", socket, "__test-mutate"}
				if source != "default" {
					token = filepath.Join(s.Home, "selected.token")
				}
				if source == "flag" {
					args = append(args, "--token-file", token)
				}
				if source == "config" {
					if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
						t.Fatal(err)
					}
					cfg := config.DefaultClient(p)
					cfg.TokenFile = token
					if err := config.WriteClient(p.ClientConfig, cfg); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(validToken)
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "missing":
				case "symlink":
					err = os.Symlink(validToken, token)
				case "directory":
					err = os.Mkdir(token, 0o700)
				case "empty":
					err = os.WriteFile(token, nil, 0o600)
				case "mode":
					err = os.WriteFile(token, data, 0o600)
					if err == nil {
						err = os.Chmod(token, 0o644)
					}
				case "crlf":
					err = os.WriteFile(token, []byte(strings.TrimSpace(string(data))+"\r\n"), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
				out, stderr, code := runCLI(t, s, "", args...)
				if kind == "crlf" {
					if code != 0 || stderr != "" {
						t.Fatalf("%d %q %q", code, out, stderr)
					}
					return
				}
				if code != 4 || out != "" {
					t.Fatalf("%d %q %q", code, out, stderr)
				}
				for _, part := range []string{token, "run `tkr init`", "--token-file", "nothing was saved. Your command was:", "  tkr " + quoteArgs(args)} {
					if !strings.Contains(stderr, part) {
						t.Errorf("missing %q in %q", part, stderr)
					}
				}
			})
		}
	}
}

// TestLocalInternalErrorShowsCause is final review B-I1: a local (offline)
// internal failure prints its sanitized cause; only the owner's own terminal
// sees it. API problems are unaffected: they never carry a cause.
func TestLocalInternalErrorShowsCause(t *testing.T) {
	cause := errors.New("backup: decode swap journal: invalid character '\x1b'")
	p := problemFor(fmt.Errorf("wrapped: %w", &domain.Error{Code: domain.CodeInternal, Err: cause}))
	if p.Code != string(domain.CodeInternal) || p.Status != http.StatusInternalServerError || p.Detail != "backup: decode swap journal: invalid character '�'" {
		t.Fatalf("problem = %+v", p)
	}
	if p := problemFor(errors.New("plain\x1b[2J failure")); p.Detail != "plain�[2J failure" {
		t.Fatalf("non-domain detail = %q", p.Detail)
	}
	if p := problemFor(&domain.Error{Code: domain.CodeInternal}); p.Detail != string(domain.CodeInternal) {
		t.Fatalf("no cause: %q", p.Detail)
	}
	// End to end: the final review's probe printed exactly "tkr: internal".
	srv, sys := captureServer(t)
	srv.Stop()
	if err := os.WriteFile(srv.Paths.SwapJournal(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, sys, "", "restore", "--resume")
	if code != 1 || stderr == "tkr: internal\n" || !strings.Contains(stderr, "decode swap journal") {
		t.Fatalf("restore --resume over a damaged journal: %d %q", code, stderr)
	}
}

// TestFailureAdviceByCommandKind is final review B-M1/B-M2: "nothing was
// saved. Your command was:" belongs to mutations only, and a failed `tkr
// pending retry` leaves its saved request pending, so it says to run it again.
func TestFailureAdviceByCommandKind(t *testing.T) {
	s, p := testSys(t)
	notSent := fmt.Errorf("%w: %w", client.ErrNotSent, &net.OpError{Op: "dial", Net: "unix", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}})
	render := func(e *env, err error) string {
		var stderr bytes.Buffer
		e.err = &stderr
		e.fail(err)
		return stderr.String()
	}
	read := &env{sys: s, paths: p, out: io.Discard, argv: []string{"ls"}, socket: "/socket"}
	if got := render(read, notSent); got != "tkr: cannot reach the Taskronin server at /socket: connection refused\ntkr: start it with: systemctl --user start taskronin.socket\n" {
		t.Fatalf("read-only not-sent = %q", got)
	}
	retry := &env{sys: s, paths: p, out: io.Discard, argv: []string{"pending", "retry"}, socket: "/socket", resending: true}
	got := render(retry, notSent)
	if strings.Contains(got, "nothing was saved") || !strings.Contains(got, "still pending") || !strings.Contains(got, "tkr pending retry") {
		t.Fatalf("pending retry not-sent = %q", got)
	}
	got = render(retry, &client.OutcomeUnknownError{Key: "request-key", Err: io.EOF})
	if strings.Contains(got, "not saved for retry") || strings.Contains(got, "check with") || !strings.Contains(got, "request-key") ||
		!strings.Contains(got, "still pending") || !strings.Contains(got, "run `tkr pending retry` again") {
		t.Fatalf("pending retry outcome-unknown = %q", got)
	}
}

// TestPendingRetryOutcomeUnknownE2E drives B-M1 through the real command: a
// resend whose response is lost again keeps the file and says so.
func TestPendingRetryOutcomeUnknownE2E(t *testing.T) {
	sys, paths := testSys(t)
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	flags := []string{"--socket", socket, "--token-file", token}
	if _, stderr, code := runCLI(t, sys, "", append([]string{"add", "x"}, flags...)...); code != 3 || !strings.Contains(stderr, "saved as pending request") {
		t.Fatalf("add: %d %q", code, stderr)
	}
	_, stderr, code := runCLI(t, sys, "", append([]string{"pending", "retry"}, flags...)...)
	if code != 3 || !strings.Contains(stderr, "still pending") || strings.Contains(stderr, "not saved for retry") || strings.Contains(stderr, "Your command was") {
		t.Fatalf("pending retry: %d %q", code, stderr)
	}
	if list, err := (&client.PendingStore{Dir: paths.PendingDir}).List(); err != nil || len(list) != 1 {
		t.Fatalf("pending after an unknown resend = %v, %v", list, err)
	}
}
