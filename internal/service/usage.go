package service

import (
	"sync"
	"time"

	"taskronin/internal/domain"
)

type usage struct {
	mu       sync.Mutex
	tokens   map[domain.TokenID]time.Time
	sessions map[[32]byte]time.Time
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
