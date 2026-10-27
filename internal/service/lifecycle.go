package service

import (
	"context"
	"sync"
	"time"

	"taskronin/internal/domain"
)

// OpBudget bounds an ordinary operation, including writer admission and views.
const OpBudget = 15 * time.Second

type lifecycle struct {
	mu       sync.Mutex
	stopping bool
	inFlight sync.WaitGroup
	stopOnce sync.Once
	ctx      context.Context
	cancel   context.CancelFunc
}

func (s *Service) stopCtx() context.Context {
	s.life.stopOnce.Do(func() { s.life.ctx, s.life.cancel = context.WithCancel(context.Background()) })
	return s.life.ctx
}

// Stopping closes when shutdown begins so callers can interrupt blocking I/O.
func (s *Service) Stopping() <-chan struct{} { return s.stopCtx().Done() }

// BeginShutdown closes admission and cancels long operations observing stopCtx.
func (s *Service) BeginShutdown() {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	// AIDEV-NOTE: Close admission under the same lock as WaitGroup.Add so
	// shutdown cannot miss a registered writer or admit one after draining.
	s.life.stopping = true
	s.stopCtx()
	s.life.cancel()
}

func (s *Service) enter() (func(), error) {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if s.life.stopping {
		return nil, domain.Errorf(domain.CodeBusy, "service is shutting down")
	}
	s.life.inFlight.Add(1)
	return s.life.inFlight.Done, nil
}

// Drain waits for accepted operations and jobs, or ctx. BeginShutdown closes
// admission before draining; a canceled drain never cancels accepted operations.
func (s *Service) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.life.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return serviceError(ctx.Err())
	}
}
