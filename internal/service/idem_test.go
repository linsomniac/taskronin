package service_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func TestIDM02(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Must(s.Svc.AddTask(s.Ctx(), s.MetaWith("key-K-0123456789ab", `{"title":"A"}`), service.AddTaskIn{Title: "A"}))
	_, err := s.Svc.AddTask(s.Ctx(), s.MetaWith("key-K-0123456789ab", `{"title":"B"}`), service.AddTaskIn{Title: "B"})
	testutil.RequireCode(t, err, domain.CodeIdemMismatch)
	if s.OpCount() != 1 {
		t.Fatal("second task created")
	}
}

func TestIDM03(t *testing.T) {
	s := testutil.NewScenario(t)
	m := s.MetaWith("key-K-0123456789ab", `{"title":"A"}`)
	first := s.Must(s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"}))
	ai := s.As(testutil.AI())
	m = ai.MetaWith(m.Idem.Key, `{"title":"C"}`)
	second := ai.Must(ai.Svc.AddTask(ai.Ctx(), m, service.AddTaskIn{Title: "C"}))
	if !first.Created || !second.Created || second.Replayed || first.Task.ID != 1 || second.Task.ID != 2 ||
		second.Task.Title != "C" || second.Task.Origin != ai.Actor.Key() || s.OpCount() != 2 {
		t.Fatalf("independent results = %+v, %+v", first, second)
	}
	ownerRow, err := store.GetIdem(s.Ctx(), s.Store.Reader, s.Actor.Key(), m.Idem.Key)
	if err != nil {
		t.Fatal(err)
	}
	aiRow, err := store.GetIdem(s.Ctx(), s.Store.Reader, ai.Actor.Key(), m.Idem.Key)
	if err != nil || ownerRow.Hash == aiRow.Hash || aiRow.Status != 201 {
		t.Fatalf("actor rows = %+v, %+v, %v", ownerRow, aiRow, err)
	}
}

func TestIDM05(t *testing.T) {
	s := testutil.NewScenario(t)
	m := s.MetaWith("key-K-0123456789ab", `{"title":"A"}`)
	first := s.Must(s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"}))
	s.Clock.Advance(7 * 24 * time.Hour)
	boundary := s.Must(s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"}))
	if !boundary.Replayed || boundary.OpID != first.OpID || s.OpCount() != 1 {
		t.Fatalf("exact TTL should replay: %+v", boundary)
	}
	s.Clock.Advance(time.Second)
	second := s.Must(s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"}))
	if second.Replayed || !second.Created || second.Task.ID != 2 || s.OpCount() != 2 {
		t.Fatalf("expired result = %+v", second)
	}
	row, err := store.GetIdem(s.Ctx(), s.Store.Reader, m.Actor.Key(), m.Idem.Key)
	if err != nil || !row.OpID.Valid || row.OpID.V != second.OpID || !row.CreatedAt.Equal(s.Clock.Now()) {
		t.Fatalf("replacement row = %+v, %v", row, err)
	}
}

func TestIDM06(t *testing.T) {
	s := testutil.NewScenario(t)
	for _, tc := range []struct {
		key  string
		code domain.Code
	}{
		{"", domain.CodeIdemRequired}, {"short", domain.CodeMalformed},
		{"0123456789abcde!", domain.CodeMalformed}, {strings.Repeat("a", 15), domain.CodeMalformed},
		{strings.Repeat("a", 129), domain.CodeMalformed}, {"0123456789abcde\n", domain.CodeMalformed},
		{strings.Repeat("é", 16), domain.CodeMalformed},
	} {
		t.Run(tc.key, func(t *testing.T) {
			_, err := s.Svc.AddTask(s.Ctx(), s.MetaWith(tc.key, `{}`), service.AddTaskIn{Title: "A"})
			testutil.RequireCode(t, err, tc.code)
		})
	}
	if s.OpCount() != 0 {
		t.Fatal("invalid keys wrote operations")
	}
	for _, key := range []string{"0123456789abcd_-", strings.Repeat("Z", 128)} {
		s.Must(s.Svc.AddTask(s.Ctx(), s.MetaWith(key, `{}`), service.AddTaskIn{Title: "A"}))
	}
}

func TestIDM10(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.MarkManual(id)
	m := s.MetaWith("manual-K2-0123456", `{}`)
	in := service.MarkManualIn{ID: id, Expect: s.Expect(id)}
	count := s.OpCount()
	first := s.Must(s.Svc.MarkManual(s.Ctx(), m, in))
	refs := []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}
	if !first.NoChange || first.Created || !first.OpID.IsZero() || !slices.Equal(first.Refs, refs) {
		t.Fatalf("no-op = %+v", first)
	}
	row, err := store.GetIdem(s.Ctx(), s.Store.Reader, m.Actor.Key(), m.Idem.Key)
	if err != nil || row.OpID.Valid || row.Status != 200 || !slices.Equal(row.Refs, refs) {
		t.Fatalf("no-op row = %+v, %v", row, err)
	}
	replay := s.Must(s.Svc.MarkManual(s.Ctx(), m, in))
	if !replay.Replayed || !replay.NoChange || replay.Created || !replay.OpID.IsZero() ||
		len(replay.Effects) != 0 || replay.Task == nil || replay.Task.ID != id || replay.Task.Version != s.Task(id).Version {
		t.Fatalf("no-op replay = %+v", replay)
	}
	m.Idem.Body = []byte(`{"different":true}`)
	_, err = s.Svc.MarkManual(s.Ctx(), m, in)
	testutil.RequireCode(t, err, domain.CodeIdemMismatch)
	if s.OpCount() != count {
		t.Fatal("no-op replay or mismatch wrote an operation")
	}
}

func TestReplayReturnsCurrent(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	m := s.MetaWith("manual-K-01234567", `{}`)
	in := service.MarkManualIn{ID: id, Expect: s.Expect(id)}
	first := s.Must(s.Svc.MarkManual(s.Ctx(), m, in))
	ref := domain.EntityRef{Entity: domain.EntTask, ID: int64(id)}
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{ref}, func(store.Q) error { return nil }))
	replay := s.Must(s.Svc.MarkManual(s.Ctx(), m, in))
	if !replay.Replayed || replay.Created || replay.NoChange || replay.OpID != first.OpID ||
		replay.Task.Version != first.Task.Version+1 || !slices.Equal(replay.Effects, first.Effects) || s.OpCount() != 3 {
		t.Fatalf("current replay = %+v, original = %+v", replay, first)
	}
}

func TestReplayRequestHash(t *testing.T) {
	s := testutil.NewScenario(t)
	m := s.MetaWith("hash-key-01234567", `{"title":"A"}`)
	m.Idem.Method, m.Idem.Route = "POST", "/tasks"
	first := s.Must(s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"}))
	auth, err := store.GetAuthState(s.Ctx(), s.Store.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := hmac.New(sha256.New, auth.IdemHMACKey[:])
	_, _ = h.Write([]byte("owner\x00POST\x00/tasks\x00" + string(m.Idem.Body)))
	row, err := store.GetIdem(s.Ctx(), s.Store.Reader, m.Actor.Key(), m.Idem.Key)
	if err != nil || !hmac.Equal(row.Hash[:], h.Sum(nil)) {
		t.Fatalf("stored hash = %x, %v", row.Hash, err)
	}
	for _, field := range []string{"method", "route", "body"} {
		t.Run(field, func(t *testing.T) {
			changed := m
			switch field {
			case "method":
				changed.Idem.Method = "PATCH"
			case "route":
				changed.Idem.Route = "/tasks/1"
			case "body":
				changed.Idem.Body = []byte(`{"title":"A","project":null}`)
			}
			_, err := s.Svc.AddTask(s.Ctx(), changed, service.AddTaskIn{Title: "A"})
			testutil.RequireCode(t, err, domain.CodeIdemMismatch)
		})
	}
	m.Actor.Channel = domain.ChanWeb
	replay := s.Must(s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"}))
	if !replay.Created || !replay.Replayed || replay.OpID != first.OpID || !slices.Equal(replay.Effects, first.Effects) || s.OpCount() != 1 {
		t.Fatalf("owner channel replay = %+v", replay)
	}
}

func TestSCP10(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("owner")
	s.MarkManual(id)
	ai := s.As(testutil.AI())
	start := s.Clock.Now()
	m := ai.Meta()
	var first *service.OpResult
	for i := range 60 {
		s.Clock.Set(start.Add(time.Duration(i) * time.Minute))
		if i == 30 {
			r := ai.Must(ai.Svc.MarkManual(ai.Ctx(), ai.Meta(), service.MarkManualIn{ID: id, Expect: s.Expect(id)}))
			if !r.NoChange {
				t.Fatal("expected a no-op")
			}
		}
		request := ai.Meta()
		if i == 0 {
			request = m
		}
		r := ai.Must(ai.Svc.AddTask(ai.Ctx(), request, service.AddTaskIn{Title: "AI"}))
		if i == 0 {
			first = r
		}
	}
	s.Clock.Advance(30 * time.Second)
	_, err := ai.Svc.AddTask(ai.Ctx(), ai.Meta(), service.AddTaskIn{Title: "over cap"})
	testutil.RequireCode(t, err, domain.CodeRateLimited)
	de, _ := domain.AsError(err)
	if de.RetryAfter != start.Add(time.Hour).Sub(s.Clock.Now()) || de.RetryAfter != 30*time.Second {
		t.Fatalf("retry after = %s", de.RetryAfter)
	}
	replay := ai.Must(ai.Svc.AddTask(ai.Ctx(), m, service.AddTaskIn{Title: "AI"}))
	if !replay.Replayed || replay.OpID != first.OpID {
		t.Fatalf("replay at cap = %+v", replay)
	}
	if _, err := ai.Svc.GetTask(ai.Ctx(), ai.Actor, id); err != nil {
		t.Fatal(err)
	}
	if n, _, err := store.CountOpsSince(s.Ctx(), s.Store.Reader, ai.Actor.Key(), start.Add(-time.Millisecond)); err != nil || n != 60 {
		t.Fatalf("actor ops = %d, %v", n, err)
	}
	s.Add("uncapped owner")
	s.Clock.Set(start.Add(time.Hour))
	ai.Add("oldest is outside window")
}

func TestGenerationChecks(t *testing.T) {
	s := testutil.NewScenario(t)
	for _, tc := range []struct {
		gen  domain.Generation
		code domain.Code
	}{
		{"", domain.CodePreconditionReq}, {"0000000000000000", domain.CodeGenerationMismatch},
	} {
		m := s.Meta()
		m.Gen = tc.gen
		_, err := s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"})
		testutil.RequireCode(t, err, tc.code)
		_, err = s.Svc.TestOpRaw(s.Ctx(), m, nil, func(store.Q) error { t.Fatal("exempt mutation ran"); return nil })
		testutil.RequireCode(t, err, tc.code)
	}
	if s.OpCount() != 0 {
		t.Fatal("generation rejection wrote an operation")
	}
	m := s.Meta()
	s.Must(s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"}))
	m.Gen = "0000000000000000"
	_, err := s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"})
	testutil.RequireCode(t, err, domain.CodeGenerationMismatch)
}

func TestExpireIdempotency(t *testing.T) {
	s := testutil.NewScenario(t)
	old := s.Meta()
	s.Must(s.Svc.AddTask(s.Ctx(), old, service.AddTaskIn{Title: "old"}))
	s.Clock.Advance(time.Second)
	boundary := s.Meta()
	s.Must(s.Svc.AddTask(s.Ctx(), boundary, service.AddTaskIn{Title: "boundary"}))
	s.Clock.Advance(7 * 24 * time.Hour)
	s.Add("fresh")
	n, err := s.Svc.ExpireIdempotency(s.Ctx())
	if err != nil || n != 1 || s.OpCount() != 3 {
		t.Fatalf("expire = %d, %v, ops = %d", n, err, s.OpCount())
	}
	if _, err := store.GetIdem(s.Ctx(), s.Store.Reader, old.Actor.Key(), old.Idem.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old row = %v", err)
	}
	if _, err := store.GetIdem(s.Ctx(), s.Store.Reader, boundary.Actor.Key(), boundary.Idem.Key); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Svc.ExpireIdempotency(s.Ctx()); err != nil || n != 0 {
		t.Fatalf("second expire = %d, %v", n, err)
	}
}

func TestReplayFailureNotStored(t *testing.T) {
	s := testutil.NewScenario(t)
	m := s.Meta()
	_, err := s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{})
	testutil.RequireCode(t, err, domain.CodeValidation)
	if _, err := store.GetIdem(s.Ctx(), s.Store.Reader, m.Actor.Key(), m.Idem.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed request persisted idempotency: %v", err)
	}
	r := s.Must(s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "valid"}))
	if r.Replayed || !r.Created || s.OpCount() != 1 {
		t.Fatalf("retry of failed request = %+v", r)
	}
	m.Actor.Scopes = domain.ScopeRead
	_, err = s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "valid"})
	testutil.RequireCode(t, err, domain.CodeForbidden)
}
