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
	clock       clock
	ids         idSource
	logger      *slog.Logger
	mutations   mutationExecution
	capacityTTL time.Duration
}

func (s *service) Run(ctx context.Context, request Request) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.run(ctx, request)
	if err != nil && result.Status == StatusComplete {
		// Complete is the zero status; a failed run never reports it.
		result.Status = statusForError(err)
	}
	if err == nil || ctx.Err() != nil {
		return result, err
	}
	return s.powerOffAfterFailure(ctx, request, result, err)
}
