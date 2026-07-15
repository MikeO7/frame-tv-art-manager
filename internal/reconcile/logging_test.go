package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestMutationRetryLogCarriesAttemptAndProtocolContext(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	service := &service{
		logger:    slog.New(slog.NewJSONHandler(&output, nil)).With("component", "reconcile"),
		mutations: mutationExecution{attempts: 3},
	}
	cause := errors.New("temporary protocol response")
	service.logMutationRetry(context.Background(), "cycle-7", CommandIntent{Kind: CommandUpload}, 1, &samsung.Error{
		Kind: samsung.ErrorKindInvalidResponse, Operation: "upload", Retryable: true,
		Outcome: samsung.OutcomeNotAttempted, Cause: cause,
	})

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode retry log: %v; output=%q", err, output.String())
	}
	want := map[string]any{
		"level": "WARN", "msg": "Samsung mutation failed; retrying", "component": "reconcile",
		"cycle_id": "cycle-7", "command": "upload", "attempt": float64(1),
		"next_attempt": float64(2), "max_attempts": float64(3),
		"error_kind": "invalid_response", "operation": "upload", "retryable": true,
		"outcome": "not_attempted",
	}
	for key, expected := range want {
		if entry[key] != expected {
			t.Errorf("retry log %s = %#v, want %#v; entry=%#v", key, entry[key], expected, entry)
		}
	}
	if got, ok := entry["error"].(string); !ok || !bytes.Contains([]byte(got), []byte(cause.Error())) {
		t.Fatalf("retry log error = %#v, want complete cause", entry["error"])
	}
}
