package offline_test

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/offline"
	"github.com/linsomniac/taskronin/internal/testutil"
)

// passwdEnv builds an offline Env over a StartServer instance's data.
func passwdEnv(t *testing.T, srv *testutil.Server, stdin string) *offline.Env {
	t.Helper()
	cfg, err := config.LoadServer(srv.Paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	return &offline.Env{Paths: srv.Paths, Server: cfg, Clock: srv.Clock, In: strings.NewReader(stdin),
		Out: new(bytes.Buffer), Err: new(bytes.Buffer)}
}

// clientWithToken returns a client for srv authenticating with the given token text.
func clientWithToken(t *testing.T, srv *testutil.Server, name, token string) *client.Client {
	t.Helper()
	path := srv.Paths.TokenFile(name)
	if err := os.MkdirAll(srv.Paths.TokensDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultClient(srv.Paths)
	cfg.Socket, cfg.TokenFile = srv.Socket, path
	c, err := client.NewFromConfig(cfg, "cli", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPasswdReset(t *testing.T) {
	srv := testutil.StartServer(t)
	before := srv.TokenClient(t, "before", domain.MCPDefaultScopes, 60, "mcp")
	oldOwner, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	password := domain.NewOpID().String() // runtime-generated (BF30)
	e := passwdEnv(t, srv, password+"\n")
	if err := offline.PasswdReset(t.Context(), e, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.Out.(*bytes.Buffer).String()+e.Err.(*bytes.Buffer).String(), password) {
		t.Fatal("the password was echoed")
	}
	info, err := os.Stat(srv.Paths.OwnerToken)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("owner.token: %v, %v", info, err)
	}
	newOwner, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil || bytes.Equal(newOwner, oldOwner) || !strings.HasSuffix(string(newOwner), "\n") {
		t.Fatalf("owner.token not rewritten: %v", err)
	}
	if bytes.Contains(newOwner, []byte(password)) {
		t.Fatal("owner.token contains the password")
	}
	srv.Start(t)
	if _, err := srv.Client.Settings(t.Context()); err != nil {
		t.Fatalf("new owner token rejected: %v", err)
	}
	old := clientWithToken(t, srv, "oldowner", string(oldOwner))
	_, err = old.Settings(t.Context())
	requireUnauthenticated(t, err)
	_, err = before.Settings(t.Context())
	requireUnauthenticated(t, err)
	// The new password works for the online verification path.
	next := domain.NewOpID().String()
	if err := srv.Client.ChangePassword(t.Context(), api.PasswordRequest{Current: password, New: next}); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

func TestPasswdResetRefusals(t *testing.T) {
	srv := testutil.StartServer(t)
	password := domain.NewOpID().String()
	err := offline.PasswdReset(t.Context(), passwdEnv(t, srv, password+"\n"), true)
	testutil.RequireCode(t, err, domain.CodeBusy)
	srv.Stop()
	before, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}

	err = offline.PasswdReset(t.Context(), passwdEnv(t, srv, "short\n"), true)
	testutil.RequireCode(t, err, domain.CodeValidation)

	err = offline.PasswdReset(t.Context(), passwdEnv(t, srv, ""), true)
	if err == nil {
		t.Fatal("empty stdin accepted")
	}

	// Without --password-stdin a non-terminal is refused, never read.
	err = offline.PasswdReset(t.Context(), passwdEnv(t, srv, password+"\n"), false)
	testutil.RequireCode(t, err, domain.CodePasswordRequired)

	// Terminal entries must match.
	tty := passwdEnv(t, srv, "")
	tty.TTY = true
	answers := []string{password, password + "x"}
	tty.ReadPassword = func(string) (string, error) {
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	testutil.RequireCode(t, offline.PasswdReset(t.Context(), tty, false), domain.CodeValidation)

	if err := os.WriteFile(srv.Paths.SwapJournal(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = offline.PasswdReset(t.Context(), passwdEnv(t, srv, password+"\n"), true)
	testutil.RequireCode(t, err, domain.CodeConflict)

	after, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("a refused reset changed owner.token: %v", err)
	}
	if err := os.Remove(srv.Paths.SwapJournal()); err != nil {
		t.Fatal(err)
	}
	srv.Start(t)
	if _, err := srv.Client.Settings(t.Context()); err != nil {
		t.Fatalf("a refused reset revoked the owner token: %v", err)
	}
}

var errCrash = errors.New("crash")

func TestPasswdResetCrashBeforeToken(t *testing.T) {
	srv := testutil.StartServer(t)
	srv.Stop()
	stale, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	password := domain.NewOpID().String()
	ctx := offline.WithCrashHook(t.Context(), func(point string) error {
		if point == "passwd:committed" {
			return errCrash
		}
		return nil
	})
	if err := offline.PasswdReset(ctx, passwdEnv(t, srv, password+"\n"), true); !errors.Is(err, errCrash) {
		t.Fatalf("crash = %v", err)
	}
	got, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil || !bytes.Equal(got, stale) {
		t.Fatalf("owner.token changed before the write: %v", err)
	}
	srv.Start(t) // the file now holds a revoked token
	_, err = srv.Client.Settings(t.Context())
	requireUnauthenticated(t, err)
	srv.Stop()

	if err := offline.PasswdReset(t.Context(), passwdEnv(t, srv, password+"\n"), true); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	srv.Start(t)
	if _, err := srv.Client.Settings(t.Context()); err != nil {
		t.Fatalf("re-run did not recover: %v", err)
	}
}

func TestPasswdResetTokenWriteFailure(t *testing.T) {
	srv := testutil.StartServer(t)
	srv.Stop()
	e := passwdEnv(t, srv, domain.NewOpID().String()+"\n")
	// A directory in the owner.token path makes the atomic rename fail.
	if err := os.Remove(srv.Paths.OwnerToken); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(srv.Paths.OwnerToken, 0o700); err != nil {
		t.Fatal(err)
	}
	err := offline.PasswdReset(t.Context(), e, true)
	if err == nil || !strings.Contains(err.Error(), "re-run `tkr passwd --reset`") {
		t.Fatalf("write failure = %v", err)
	}
}

func requireUnauthenticated(t *testing.T, err error) {
	t.Helper()
	var problem *client.ProblemError
	if !errors.As(err, &problem) || problem.Code() != domain.CodeUnauthenticated {
		t.Fatalf("error = %v, want unauthenticated", err)
	}
}
