package samsung

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func (c *Client) registerImageAddedListener() (waitFn func(ctx context.Context, timeout time.Duration) (string, error)) {
	ch := make(chan json.RawMessage, 1)

	c.artConn.pendingMu.Lock()
	c.artConn.pending["image_added"] = ch
	c.artConn.pendingMu.Unlock()

	return func(ctx context.Context, timeout time.Duration) (string, error) {
		defer func() {
			if c.artConn != nil {
				c.artConn.pendingMu.Lock()
				delete(c.artConn.pending, "image_added")
				c.artConn.pendingMu.Unlock()
			}
		}()

		select {
		case data, ok := <-ch:
			if !ok {
				return "", ErrNotConnected
			}
			var resp artResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				return "", fmt.Errorf("parse image_added: %w", err)
			}
			if err := checkArtError(&resp); err != nil {
				return "", fmt.Errorf("image_added error: %w", err)
			}
			return resp.ContentID, nil
		case <-time.After(timeout):
			return "", fmt.Errorf("%w: waiting for image_added", ErrTimeout)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// upload sends an image to the TV via the art API + D2D socket transfer.
//
//nolint:funlen // complexity justified for this domain-specific path
func (c *Client) upload(ctx context.Context, filePath, fileType, matte string) (string, error) {
	stat, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", filePath, err)
	}

	if matte == "" {
		matte = c.opts.MatteStyle
	}

	// Register the image_added listener BEFORE sending the upload request,
	// so we don't miss the response if it arrives quickly.
	waitForAdded := c.registerImageAddedListener()

	// Step 1: Send the upload request to get D2D connection info.
	id := newRequestID()
	artReq := map[string]any{
		keyRequest:   "send_image",
		"file_type":  fileType,
		"id":         id,
		keyRequestID: id,
		"conn_info": map[string]any{
			"d2d_mode":      "socket",
			"connection_id": time.Now().UnixNano() % (4 * 1024 * 1024 * 1024),
			"id":            id,
		},
		"image_date":        time.Now().Format("2006:01:02 15:04:05"),
		"matte_id":          matte,
		"portrait_matte_id": matte,
		"file_size":         stat.Size(),
	}

	payload, err := artAppRequest(artReq)
	if err != nil {
		return "", fmt.Errorf("build send_image request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
	if err != nil {
		return "", fmt.Errorf("send_image: %w", err)
	}

	c.logger.Debug("send_image raw response", "raw", string(raw))
	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parse send_image response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return "", fmt.Errorf("send_image error: %w", err)
	}

	if resp.ConnInfo == "" {
		return "", fmt.Errorf("send_image: no conn_info in response")
	}

	c.logger.Debug("send_image conn_info string", "conn_info", resp.ConnInfo)
	var ci connInfo
	if err := json.Unmarshal([]byte(resp.ConnInfo), &ci); err != nil {
		return "", fmt.Errorf("parse conn_info: %w", err)
	}
	c.logger.Debug("send_image parsed conn_info", "ip", ci.IP, "port", ci.Port)

	// Step 2: Transfer the file over D2D socket.
	if err := uploadImageD2D(ctx, ci, filePath, fileType, c.opts.ConnectionTimeout); err != nil {
		return "", fmt.Errorf("d2d transfer: %w", err)
	}

	// Step 3: Wait for the TV to confirm the image was added.
	contentID, err := waitForAdded(ctx, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("wait for confirmation: %w", err)
	}

	return contentID, nil
}
