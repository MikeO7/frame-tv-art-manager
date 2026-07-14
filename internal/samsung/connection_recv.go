package samsung

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// recvLoop reads messages from the WebSocket and routes them to pending
// request channels based on request_id or event name.
func (c *connection) recvLoop() {
	defer close(c.recvDone)

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return
	}

	for {
		_, msg, err := conn.Read(context.Background())
		if err != nil {
			if !c.closed.Load() {
				c.logger.Debug("recv loop error", "error", err)
			}
			return
		}

		var resp wsResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			c.logger.Debug("recv: unparseable message", "error", err)
			continue
		}

		// Route d2d service messages to pending requests.
		// Support both dot-notation and underscore-notation used by different models.
		if resp.Event == EventD2DServiceMessageEvent || resp.Event == EventD2DServiceMessage {
			c.routeD2DEvent(resp.Data)
		}
	}
}

func (c *connection) routeD2DEvent(dataRaw json.RawMessage) {
	// Some TVs (like the 2024 model) send 'data' as a JSON-encoded string.
	// Others send it as a raw JSON object. We try to handle both.
	var dataToParse []byte = dataRaw

	var dataStr string
	if err := json.Unmarshal(dataRaw, &dataStr); err == nil {
		// It was a string! Use the unwrapped string content for parsing.
		dataToParse = []byte(dataStr)
	}

	var inner struct {
		RequestID string `json:"request_id"`
		ID        string `json:"id"`
		Event     string `json:"event"`
	}
	if err := json.Unmarshal(dataToParse, &inner); err != nil {
		c.logger.Debug("d2d event: parse failed", "error", err)
		return
	}

	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	// Try matching by request_id first, then event name.
	keys := []string{inner.RequestID, inner.ID, inner.Event}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if ch, ok := c.pending[key]; ok {
			select {
			case ch <- dataToParse:
			default:
			}
			return
		}
	}
}

// wsResponse is the top-level WebSocket message envelope from the TV.
type wsResponse struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// JSON envelope keys shared by outgoing art-app request messages.
const (
	keyMethod = "method"
	keyParams = "params"
	keyEvent  = "event"
	keyData   = "data"
)

// artAppRequest builds the outer "ms.channel.emit" WebSocket message that wraps
// an art API request payload, JSON-encoding the inner data as the host expects.
func artAppRequest(data map[string]any) ([]byte, error) {
	inner, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	outer := map[string]any{
		keyMethod: "ms.channel.emit",
		keyParams: map[string]any{
			keyEvent: "art_app_request",
			"to":     "host",
			keyData:  string(inner),
		},
	}
	return json.Marshal(outer)
}

// newRequestID generates a new UUID string for art API request correlation.
var requestIDFallbackSequence atomic.Uint64 //nolint:gochecknoglobals // monotonic fallback prevents correlation collisions

func newRequestID() string {
	return requestIDFrom(rand.Reader)
}

func requestIDFrom(random io.Reader) string {
	b := make([]byte, 16)
	if _, err := io.ReadFull(random, b); err != nil {
		seed := fmt.Sprintf("%d:%d", time.Now().UnixNano(), requestIDFallbackSequence.Add(1))
		digest := sha256.Sum256([]byte(seed))
		copy(b, digest[:16])
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
