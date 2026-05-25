package samsung

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// saveMetadata fetches all available system information and artwork categories,
// saving them to a JSON file in the tokens directory for auditing.
func (c *Client) saveMetadata(ctx context.Context) error {
	metadata := make(map[string]any)
	metadata["timestamp"] = time.Now().Format(time.RFC3339)

	// 1. Basic Device Info.
	if c.info != nil {
		metadata["device"] = c.info
	}

	// 2. Slideshow Status.
	if ss, err := c.slideshowStatus(ctx); err == nil {
		metadata["slideshow"] = ss
	}

	// 3. All Categories.
	if cats, err := c.getCategories(ctx); err == nil {
		var raw json.RawMessage
		if err := json.Unmarshal(cats, &raw); err == nil {
			metadata["categories"] = raw
		}
	}

	// 4. Detailed Environment (reserved for future telemetry integration).
	metadata["platform"] = "Y2025"

	b, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	safeIP := strings.ReplaceAll(c.IP, ".", "_")
	path := filepath.Join(c.opts.TokenDir, fmt.Sprintf("tv_%s_metadata.json", safeIP))

	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write metadata file: %w", err)
	}

	c.logger.Info("metadata saved", "path", path)
	return nil
}
