package service

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
)

type usage struct {
	flushMu  sync.RWMutex
	mu       sync.Mutex
	tokens   map[domain.TokenID]time.Time
	sessions map[[32]byte]time.Time
}

// FlushUsage persists coalesced token and session activity, including after Drain.
func (s *Service) FlushUsage(ctx context.Context) error {
	// AIDEV-NOTE: Serialize flushes with session authentication so swapping
	// the maps cannot hide recent activity before its transaction commits.
	// Token authentication can keep recording into the replacement map.
	s.usage.flushMu.Lock()
	defer s.usage.flushMu.Unlock()
	s.usage.mu.Lock()
	tokens, sessions := s.usage.tokens, s.usage.sessions
	s.usage.tokens = make(map[domain.TokenID]time.Time)
	s.usage.sessions = make(map[[32]byte]time.Time)
	s.usage.mu.Unlock()

	err := s.maintTx(ctx, func(tx *sql.Tx, _ time.Time) error {
		if err := store.TouchTokens(ctx, tx, tokens); err != nil {
			return err
		}
		return store.TouchSessions(ctx, tx, sessions)
	})
	if err != nil {
		// Preserve failed batches for retry without overwriting newer use.
		s.usage.mu.Lock()
		defer s.usage.mu.Unlock()
		for id, at := range tokens {
			if previous, ok := s.usage.tokens[id]; !ok || at.After(previous) {
				s.usage.tokens[id] = at
			}
		}
		for id, at := range sessions {
			if previous, ok := s.usage.sessions[id]; !ok || at.After(previous) {
				s.usage.sessions[id] = at
			}
		}
	}
	return err
}

func (s *Service) noteTokenUse(id domain.TokenID, at time.Time) {
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	if s.usage.tokens == nil {
		s.usage.tokens = make(map[domain.TokenID]time.Time)
	}
	if previous, ok := s.usage.tokens[id]; !ok || at.After(previous) {
		s.usage.tokens[id] = at
	}
}
