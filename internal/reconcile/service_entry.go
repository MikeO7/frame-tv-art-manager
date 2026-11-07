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
	result, err := s.run(ctx, request)
	if err == nil || ctx.Err() != nil {
		return result, err
	}
	return s.powerOffAfterFailure(ctx, request, result, err)
}
