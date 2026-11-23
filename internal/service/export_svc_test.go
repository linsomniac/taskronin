package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func TestExport(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Log(id, "did", "")
	var buf bytes.Buffer
	if err := s.Svc.Export(s.Ctx(), testutil.Owner(), &buf); err != nil {
		t.Fatal(err)
	}
	var f store.ExportFile
	if err := json.Unmarshal(buf.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if f.Format != "taskronin-export" || f.Version != 1 || f.SchemaVersion != 1 || f.Sequences["tasks"] != 1 ||
		len(f.Tables["tasks"]) != 1 || len(f.Tables["log_entries"]) != 1 || f.Settings["backup_keep_daily"] != "7" ||
		f.ExportedAt != "2026-10-28T10:00:00.000-06:00" || f.DBGeneration != string(s.Svc.Generation()) {
		t.Fatalf("export = %+v", f)
	}
	for _, table := range []string{"auth_state", "api_tokens", "web_sessions", "idempotency", "schema_migrations"} {
		if _, ok := f.Tables[table]; ok {
			t.Fatalf("export contains table %s", table)
		}
	}
	for _, secret := range []string{"password_hash", "secret_hash", "idem_hmac_key", "$argon2id$"} {
		if bytes.Contains(buf.Bytes(), []byte(secret)) {
			t.Fatalf("export contains %s", secret)
		}
	}
	if len(f.Tables) != len(store.ExportTables) || len(f.Settings) != len(store.ExportSettings) {
		t.Fatal("export allowlists differ")
	}
	for _, table := range store.ExportTables {
		if rows, ok := f.Tables[table]; !ok || rows == nil {
			t.Fatalf("missing table array %s", table)
		}
	}
	for _, key := range store.ExportSettings {
		if f.Settings[key] != backupSetting(t, s, key) {
			t.Fatalf("setting %s differs", key)
		}
	}
}

type exportWriterFunc func([]byte) (int, error)

func (f exportWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestExportSnapshotConsistency(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("before")
	var buf bytes.Buffer
	changed := false
	err := s.Svc.Export(s.Ctx(), testutil.Owner(), exportWriterFunc(func(p []byte) (int, error) {
		if !changed {
			changed = true
			s.Add("after")
			weekly := 5
			if _, err := s.Svc.UpdateSettings(s.Ctx(), s.Meta(), service.UpdateSettingsIn{KeepWeekly: &weekly}); err != nil {
				return 0, err
			}
		}
		return buf.Write(p)
	}))
	if err != nil {
		t.Fatal(err)
	}
	var f store.ExportFile
	if err := json.Unmarshal(buf.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if f.Sequences["tasks"] != 1 || len(f.Tables["tasks"]) != 1 || f.Tables["tasks"][0]["title"] != "before" ||
		f.Settings["backup_keep_weekly"] != "4" || len(f.Tables["ops"]) != 1 {
		t.Fatalf("inconsistent reader snapshot: %+v", f)
	}
}

func TestExportWriterError(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("A")
	for _, afterRow := range []bool{false, true} {
		want := errors.New("writer closed")
		err := s.Svc.Export(s.Ctx(), testutil.Owner(), exportWriterFunc(func(p []byte) (int, error) {
			if !afterRow || bytes.Contains(p, []byte(`"title":"A"`)) {
				return 0, want
			}
			return len(p), nil
		}))
		if err != want { //nolint:errorlint // Export must return the writer's original error unchanged.
			t.Fatalf("writer error was changed: %v", err)
		}
		if n := s.Store.Reader.Stats().InUse; n != 0 {
			t.Fatalf("export leaked %d reader connections", n)
		}
	}
}

func TestExportCallerCancellation(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("A")
	s.Add("B")
	ctx, cancel := context.WithCancel(s.Ctx())
	defer cancel()
	var buf bytes.Buffer
	err := s.Svc.Export(ctx, testutil.Owner(), exportWriterFunc(func(p []byte) (int, error) {
		if bytes.Contains(p, []byte(`"title":"A"`)) {
			cancel()
		}
		return buf.Write(p)
	}))
	if !errors.Is(err, context.Canceled) || bytes.Contains(buf.Bytes(), []byte(`"title":"B"`)) {
		t.Fatalf("export ignored cancellation between rows: %v", err)
	}
}

func TestExportAuthorization(t *testing.T) {
	s := testutil.NewScenario(t)
	for _, a := range []domain.Actor{testutil.TokenActor("reader", domain.ScopeRead), domain.SystemActor()} {
		var buf bytes.Buffer
		err := s.Svc.Export(s.Ctx(), a, &buf)
		testutil.RequireCode(t, err, domain.CodeForbidden)
		if buf.Len() != 0 {
			t.Fatal("unauthorized export wrote output")
		}
	}
}

func TestExportStopsOnShutdown(t *testing.T) {
	s := testutil.NewScenario(t)
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() { s.Svc.BeginShutdown(); unblock() })
	result := make(chan error, 1)
	want := errors.New("output interrupted by shutdown")
	go func() {
		result <- s.Svc.Export(s.Ctx(), testutil.Owner(), exportWriterFunc(func([]byte) (int, error) {
			close(started)
			<-s.Svc.Stopping()
			<-release
			return 0, want
		}))
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("export never started writing")
	}
	s.Svc.BeginShutdown()
	short, cancel := context.WithCancel(s.Ctx())
	cancel()
	if err := s.Svc.Drain(short); !errors.Is(err, context.Canceled) {
		t.Fatalf("Drain returned before export unwound: %v", err)
	}
	unblock()
	select {
	case err := <-result:
		if err != want { //nolint:errorlint // Export must return the writer's original error unchanged.
			t.Fatalf("shutdown write error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("export did not stop")
	}
	ctx, cancel := context.WithTimeout(s.Ctx(), 5*time.Second)
	defer cancel()
	if err := s.Svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if n := s.Store.Reader.Stats().InUse; n != 0 {
		t.Fatalf("export leaked %d reader connections", n)
	}
	testutil.RequireCode(t, s.Svc.Export(s.Ctx(), testutil.Owner(), io.Discard), domain.CodeBusy)
}

func TestRecordSystemOp(t *testing.T) {
	for _, kind := range []domain.OpKind{domain.OpSystemRestore, domain.OpSystemImport} {
		t.Run(string(kind), func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			block := s.Block(id, "11:00-12:00")
			s.SetFocus(id, false)
			focus := s.Focus()
			s.At("2026-10-29 13:00")
			before := s.OpCount()
			after := json.RawMessage(`{"schema_version":1}`)
			r, err := s.Svc.RecordSystemOp(s.Ctx(), kind, after)
			if err != nil {
				t.Fatal(err)
			}
			op := s.Op(r.OpID)
			if op.Kind != kind || op.Actor != "system" || op.Channel != domain.ChanSystem || op.Scopes != 0 || op.Revertible || s.OpCount() != before+1 {
				t.Fatalf("system op = %+v", op)
			}
			events := s.Events(r.OpID)
			if len(events) != 1 {
				t.Fatalf("system events = %+v", events)
			}
			e := events[0]
			if e.Entity != domain.EntSetting || e.Action != domain.ActUpdate || e.EntityID != 0 || e.TaskID.Valid || e.Derived ||
				len(e.Before) != 0 || !bytes.Equal(e.After, after) || !reflect.DeepEqual(s.EventKeys(r.OpID), []string{"setting:update"}) {
				t.Fatalf("system event = %+v", e)
			}
			b, err := store.GetBlock(s.Ctx(), s.Store.Reader, block)
			if err != nil || b.Status != domain.BlockPlanned {
				t.Fatalf("system op reconciled historical block: %+v, %v", b, err)
			}
			if got := s.Focus(); !reflect.DeepEqual(got, focus) {
				t.Fatalf("system op changed historical focus: %+v, want %+v", got, focus)
			}
			if err := s.Svc.Reconcile(s.Ctx()); err != nil {
				t.Fatal(err)
			}
			if err := service.CheckAll(s.Ctx(), s.Store.Reader, s.Clock.Now(), testutil.Zone); err != nil {
				t.Fatal(err)
			}
		})
	}
	s := testutil.NewScenario(t)
	_, err := s.Svc.RecordSystemOp(s.Ctx(), domain.OpTaskAdd, json.RawMessage(`{}`))
	testutil.RequireCode(t, err, domain.CodeValidation)
	for _, invalid := range []string{"", "null", "[]", `"text"`, "1", "{"} {
		_, err := s.Svc.RecordSystemOp(s.Ctx(), domain.OpSystemRestore, json.RawMessage(invalid))
		testutil.RequireCode(t, err, domain.CodeValidation)
	}
	if s.OpCount() != 0 {
		t.Fatal("invalid system op wrote history")
	}
}
