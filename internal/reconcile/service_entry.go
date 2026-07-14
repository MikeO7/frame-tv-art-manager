package reconcile

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type service struct {
	mu          sync.Mutex
	store       stateStore
	legacy      legacyMappingStore
	policy      Policy
	clock       Clock
	ids         IDSource
	logger      *slog.Logger
	mutations   mutationExecution
	capacityTTL time.Duration
}

func (s *service) Run(ctx context.Context, request Request) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run(ctx, request)
}
