package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckArtErrorClassifiesResponses(t *testing.T) {
	tests := []struct {
		name string
		code int
		want error
	}{
		{name: "success", code: 0},
		{name: "forbidden response", code: 403, want: ErrArtAPIError},
		{name: "insufficient storage", code: 507, want: ErrStorageFull},
		{name: "Samsung storage code", code: 11001, want: ErrStorageFull},
		{name: "generic API error", code: 42, want: ErrArtAPIError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkArtError(&artResponse{ErrorCode: tt.code})
			if tt.want == nil && err != nil {
				t.Fatalf("checkArtError: %v", err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("checkArtError() = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestParsePolyStringVariants(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "empty"},
		{name: "null", raw: json.RawMessage("null")},
		{name: "escaped string", raw: json.RawMessage(`"{\"id\":1}"`), want: `{"id":1}`},
		{name: "object", raw: json.RawMessage(`{"id":1}`), want: `{"id":1}`},
		{name: "array", raw: json.RawMessage(`[1,2]`), want: `[1,2]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePolyString(tt.raw); got != tt.want {
				t.Fatalf("parsePolyString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractAndSaveTokenErrorPaths(t *testing.T) {
	logger := slog.Default()
	c := newConnection(connConfig{tokenFile: filepath.Join(t.TempDir(), "token.txt"), logger: logger})
	if err := c.extractAndSaveToken(context.Background(), json.RawMessage(`{`)); err != nil {
		t.Fatalf("malformed token payload: %v", err)
	}
	if err := c.extractAndSaveToken(context.Background(), json.RawMessage(`{"token":""}`)); err != nil {
		t.Fatalf("empty token payload: %v", err)
	}
	if _, err := os.Stat(c.tokenFile); !os.IsNotExist(err) {
		t.Fatalf("invalid or empty tokens created a file: %v", err)
	}

	blocked := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.tokenFile = filepath.Join(blocked, "token.txt")
	if err := c.extractAndSaveToken(context.Background(), json.RawMessage(`{"token":"abcdefghijk"}`)); err == nil {
		t.Fatal("blocked token path error = nil")
	}
}
