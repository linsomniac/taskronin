package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func TestShutdown(t *testing.T) {
	s := testutil.NewScenario(t)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	done := make(chan struct{})
	var opErr error
	go func() {
		_, opErr = s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(store.Q) error {
			close(started)
			<-release
			return nil
		})
		close(done)
	}()
	t.Cleanup(func() { unblock(); <-done })
	select {
	case <-started:
	case <-done:
		t.Fatalf("operation failed before blocking: %v", opErr)
	}
	stopping := s.Svc.Stopping()
	select {
	case <-stopping:
		t.Fatal("premature shutdown")
	default:
	}
	s.Svc.BeginShutdown()
	s.Svc.BeginShutdown()
	select {
	case <-stopping:
	default:
		t.Fatal("shutdown did not close Stopping")
	}
	_, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "late"})
	testutil.RequireCode(t, err, domain.CodeBusy)
	testutil.RequireCode(t, s.Svc.Reconcile(s.Ctx()), domain.CodeBusy)
	_, err = s.Svc.ExpireIdempotency(s.Ctx())
	testutil.RequireCode(t, err, domain.CodeBusy)
	ctx, cancel := context.WithCancel(s.Ctx())
	cancel()
	if err := s.Svc.Drain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("drain did not wait for in-flight op: %v", err)
	}
	unblock()
	ctx, cancel = context.WithTimeout(s.Ctx(), 5*time.Second)
	defer cancel()
	if err := s.Svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	if opErr != nil {
		t.Fatal(opErr)
	}
	if s.OpCount() != 1 {
		t.Fatal("accepted operation failed to commit during shutdown")
	}
}

func TestShutdownDetachedOperation(t *testing.T) {
	s := testutil.NewScenario(t)
	ctx, cancel := context.WithCancel(s.Ctx())
	cancel()
	r := s.Must(s.Svc.AddTask(ctx, s.Meta(), service.AddTaskIn{Title: "detached", Tags: []string{"tag"}}))
	if !r.Created || r.Task == nil || r.Task.Title != "detached" || s.OpCount() != 1 {
		t.Fatalf("canceled caller result = %+v", r)
	}
	_, err := s.Svc.GetTask(ctx, s.Actor, r.Task.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read lost caller cancellation: %v", err)
	}
}

func TestConcurrentOps(t *testing.T) {
	s := testutil.NewScenario(t)
	start := make(chan struct{})
	firstDone := make(chan struct{})
	errs := make(chan error, 25)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			<-start
			_, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "concurrent"})
			errs <- err
			if i == 0 {
				close(firstDone)
			}
		})
	}
	for range 5 {
		wg.Go(func() {
			<-start
			if err := s.Svc.Reconcile(s.Ctx()); err != nil {
				errs <- err
				return
			}
			// Wait for a committed task while the other writers are still active.
			<-firstDone
			_, err := s.Svc.GetTask(s.Ctx(), s.Actor, 1)
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count, ranks int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*), COUNT(DISTINCT rank) FROM tasks`).Scan(&count, &ranks); err != nil {
		t.Fatal(err)
	}
	if count != 20 || ranks != 20 || s.OpCount() != 20 {
		t.Fatalf("tasks/ranks/ops = %d/%d/%d", count, ranks, s.OpCount())
	}
	if err := service.CheckAll(s.Ctx(), s.Store.Reader, s.Clock.Now(), s.Svc.Zone()); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentReplay(t *testing.T) {
	s := testutil.NewScenario(t)
	m := s.Meta()
	var wg sync.WaitGroup
	results := make(chan *service.OpResult, 20)
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			r, err := s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "same request"})
			results <- r
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	replays := 0
	for r := range results {
		if !r.Created || r.Task.ID != 1 {
			t.Fatalf("result = %+v", r)
		}
		if r.Replayed {
			replays++
		}
	}
	if replays != 19 || s.OpCount() != 1 {
		t.Fatalf("replays/ops = %d/%d", replays, s.OpCount())
	}
}
