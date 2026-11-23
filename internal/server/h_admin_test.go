package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/auth"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/lock"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func TestTokensLifecycle(t *testing.T) {
	srv := testutil.StartServer(t)
	in := api.CreateTokenRequest{Name: "ai", Scopes: "read,capture,write", ExpiresAt: api.Opt[api.Time]{Set: true, Null: true}, MutationsPerHour: 60}
	created, err := srv.Client.CreateToken(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Secret, fmt.Sprintf("tkr_%d_", created.Token.ID)) || created.Token.Name != in.Name || created.Token.Scopes != in.Scopes || created.Token.ExpiresAt != nil || created.Token.MutationsPerHour != 60 {
		t.Fatal("incorrect token creation metadata")
	}
	_, err = srv.Client.CreateToken(t.Context(), in)
	requireReadProblem(t, err, 409, domain.CodeConflict, "")
	tokens, err := srv.Client.Tokens(t.Context())
	if err != nil || len(tokens) != 2 {
		t.Fatalf("tokens = %+v, %v", tokens, err)
	}
	status, _, body := readAPI(t, srv, "/tokens", "")
	if status != 200 || bytes.Contains(body, []byte("secret")) || bytes.Contains(body, []byte("hash")) {
		t.Fatal("token listing leaks credentials or failed")
	}
	for _, tok := range tokens {
		if tok.Owner {
			_, err = srv.Client.RevokeToken(t.Context(), client.Mut{}, tok.ID)
			requireReadProblem(t, err, 409, domain.CodeConflict, "")
		}
	}
	m := client.Mut{Key: api.NewIdempotencyKey()}
	for range 2 {
		revoked, err := srv.Client.RevokeToken(t.Context(), m, created.Token.ID)
		if err != nil || revoked.RevokedAt == nil || revoked.ID != created.Token.ID {
			t.Fatalf("revoke = %+v, %v", revoked, err)
		}
	}
	status, headers, body := mutateAPI(t, srv, "POST", "/tokens", `{"name":"raw","scopes":"admin","expires_at":null,"mutations_per_hour":0}`, "", http.Header{api.HeaderIdempotencyKey: nil})
	if status != 201 || headers.Get("Location") == "" {
		t.Fatalf("create status=%d headers=%v body=%s", status, headers, body)
	}
	for _, body := range []string{`{"name":"invalid","scopes":"read"}`, `{"name":"invalid","scopes":"bogus","expires_at":null}`} {
		status, _, data := mutateAPI(t, srv, "POST", "/tokens", body, "", nil)
		taskProblem(t, status, 422, data, domain.CodeValidation, "")
	}
}

func TestChangePassword(t *testing.T) {
	srv := testutil.StartServer(t)
	secret, err := auth.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	next := auth.FormatToken(1, secret)
	err = srv.Client.ChangePassword(t.Context(), api.PasswordRequest{Current: "invalid", New: next})
	requireReadProblem(t, err, 403, domain.CodePasswordInvalid, "")
	if err := srv.Client.ChangePassword(t.Context(), api.PasswordRequest{Current: testutil.Password, New: next}); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(api.PasswordRequest{Current: next, New: testutil.Password})
	if err != nil {
		t.Fatal(err)
	}
	status, _, body := mutateAPI(t, srv, "POST", "/password", string(data), "", http.Header{api.HeaderIdempotencyKey: nil})
	if status != 204 || len(body) != 0 {
		t.Fatalf("password status=%d body=%s", status, body)
	}
	assertAdminSecretsAbsent(t, srv, testutil.Password, next)
}

func assertAdminSecretsAbsent(t *testing.T, srv *testutil.Server, secrets ...string) {
	t.Helper()
	st, err := store.Open(t.Context(), srv.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	var dump bytes.Buffer
	for _, query := range []string{`SELECT * FROM idempotency`, `SELECT * FROM ops`, `SELECT * FROM events`} {
		rows, err := st.Reader.QueryContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range values {
				if b, ok := v.([]byte); ok {
					dump.Write(b)
				} else {
					_, _ = fmt.Fprint(&dump, v)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM idempotency`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("exempt requests saved idempotency: %d, %v", count, err)
	}
	for _, secret := range secrets {
		if strings.Contains(srv.Log.String(), secret) || strings.Contains(dump.String(), secret) {
			t.Fatal("password leaked to logs or audit/idempotency storage")
		}
	}
}

func TestSettingsLowering(t *testing.T) {
	srv := testutil.StartServer(t)
	daily := 5
	_, err := srv.Client.UpdateSettings(t.Context(), api.SettingsRequest{BackupKeepDaily: &daily})
	requireReadProblem(t, err, 403, domain.CodePasswordRequired, "")
	got, err := srv.Client.UpdateSettings(t.Context(), api.SettingsRequest{BackupKeepDaily: &daily, Password: testutil.Password})
	if err != nil || *got != (api.Settings{BackupKeepDaily: 5, BackupKeepWeekly: 4}) {
		t.Fatalf("lower=%+v, %v", got, err)
	}
	daily = 9
	got, err = srv.Client.UpdateSettings(t.Context(), api.SettingsRequest{BackupKeepDaily: &daily})
	if err != nil || got.BackupKeepDaily != 9 {
		t.Fatalf("raise=%+v, %v", got, err)
	}
	daily = 2
	_, err = srv.Client.UpdateSettings(t.Context(), api.SettingsRequest{BackupKeepDaily: &daily})
	requireReadProblem(t, err, 422, domain.CodeValidation, "")
	assertAdminSecretsAbsent(t, srv, testutil.Password)
}

func TestBackupsHTTP(t *testing.T) {
	srv := testutil.StartServer(t)
	snapshot, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(snapshot.Path)
	if err != nil || stat.Mode().Perm() != 0o600 || snapshot.Kind != "manual" {
		t.Fatalf("snapshot=%+v stat=%v err=%v", snapshot, stat, err)
	}
	status, err := srv.Client.Backups(t.Context())
	if err != nil || !status.Healthy {
		t.Fatalf("backups=%+v %v", status, err)
	}
	found := false
	for _, s := range status.Snapshots {
		found = found || s.Name == snapshot.Name
	}
	if !found {
		t.Fatal("manual backup absent")
	}
	srv.Clock.Advance(time.Second)
	code, headers, body := mutateAPI(t, srv, "POST", "/backups", `{}`, "", http.Header{api.HeaderIdempotencyKey: nil})
	if code != 201 || headers.Get("Location") != "/api/v1/backups" {
		t.Fatalf("backup HTTP=%d headers=%v body=%s", code, headers, body)
	}
	for _, tc := range []struct {
		gen    string
		status int
		code   domain.Code
	}{{"", 428, domain.CodePreconditionReq}, {string(domain.NewGeneration()), 412, domain.CodeGenerationMismatch}} {
		code, _, body := mutateAPI(t, srv, "POST", "/backups", `{}`, "", http.Header{api.HeaderGeneration: {tc.gen}})
		taskProblem(t, code, tc.status, body, tc.code, "")
	}
	after, err := srv.Client.Backups(t.Context())
	if err != nil || len(after.Snapshots) != len(status.Snapshots)+1 {
		t.Fatalf("invalid generation created backup: %+v %v", after, err)
	}
}

func TestExportStream(t *testing.T) {
	srv := testutil.StartServer(t)
	addHTTPTask(t, srv, `{"title":"exported task"}`)
	var buf bytes.Buffer
	if err := srv.Client.Export(t.Context(), &buf); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil || doc.Format != "taskronin-export" || doc.Version != 1 {
		t.Fatalf("export header=%+v %v", doc, err)
	}
	status, headers, body := readAPI(t, srv, "/export", "")
	if status != 200 || headers.Get("Content-Type") != "application/json" || headers.Get("Content-Disposition") != `attachment; filename="taskronin-export.json"` || !json.Valid(body) {
		t.Fatalf("export status=%d headers=%v", status, headers)
	}
}

func TestExportStalledReaderDoesNotBlockShutdown(t *testing.T) {
	srv := testutil.StartServer(t)
	for range 40 {
		if _, err := srv.Svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), srv.Svc.Generation()), service.AddTaskIn{Title: "large export", Notes: strings.Repeat("n", 64<<10)}); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "unix", srv.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	token, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "GET /api/v1/export HTTP/1.1\r\nHost: taskronin\r\nAuthorization: Bearer %s\r\n\r\n", strings.TrimSpace(string(token))); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	done := make(chan struct{})
	go func() { srv.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = conn.Close()
		<-done
		t.Fatal("stalled export blocked shutdown")
	}
	held, err := lock.Acquire(srv.Paths.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	// AIDEV-NOTE: Bound real socket I/O independently of the frozen domain clock.
	if err := conn.SetReadDeadline(clock.Real{}.Now().Add(slowRunnerWait)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || err == nil || json.Valid(body) || bytes.HasSuffix(bytes.TrimSpace(body), []byte("}")) {
		t.Fatalf("expected truncated export: status=%d len=%d err=%v", resp.StatusCode, len(body), err)
	}
}

// Verify the public streaming method propagates caller cancellation as well.
func TestExportCanceled(t *testing.T) {
	srv := testutil.StartServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := srv.Client.Export(ctx, io.Discard); err == nil {
		t.Fatal("canceled export succeeded")
	}
}
