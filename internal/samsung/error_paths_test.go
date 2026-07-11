package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestCheckArtErrorClassifiesResponses(t *testing.T) {
	tests := []struct {
		name string
		code int
		want error
	}{
		{name: "success", code: 0},
		{name: "forbidden storage response", code: 403, want: ErrStorageFull},
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

func TestClientGateAndExistingTokenFastPaths(t *testing.T) {
	c := NewClient("127.0.0.1", (&config.Config{}).TVConnectOptions(), slog.Default())
	if err := c.checkGate(context.Background()); err != nil {
		t.Fatalf("disabled checkGate: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenFile, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := c.setupToken(ctx, tokenFile); err != nil {
		t.Fatalf("setupToken existing token: %v", err)
	}
}

func TestExtractAndSaveTokenErrorPaths(t *testing.T) {
	logger := slog.Default()
	c := newConnection(connConfig{tokenFile: filepath.Join(t.TempDir(), "token.txt"), logger: logger})
	c.extractAndSaveToken(json.RawMessage(`{`))
	c.extractAndSaveToken(json.RawMessage(`{"token":""}`))
	if _, err := os.Stat(c.tokenFile); !os.IsNotExist(err) {
		t.Fatalf("invalid or empty tokens created a file: %v", err)
	}

	blocked := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.tokenFile = filepath.Join(blocked, "token.txt")
	c.extractAndSaveToken(json.RawMessage(`{"token":"abcdefghijk"}`))
}
