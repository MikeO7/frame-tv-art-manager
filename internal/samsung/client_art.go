package samsung

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// imageAddedTimeout bounds how long Upload waits for the TV to confirm an
// uploaded image via the "image_added" event after the D2D transfer completes.
const imageAddedTimeout = 30 * time.Second

// IsInArtMode reports whether the TV is currently in art mode by querying
// the art API over the active WebSocket connection. On query failure it
// returns true, treating the TV as safe to sync.
func (c *Client) IsInArtMode(ctx context.Context) bool {
	if c.info != nil && !c.info.IsOn() {
		c.logger.Debug("TV is powered off")
		return false
	}

	id := newRequestID()
	req := map[string]any{
		keyRequest:   keyGetArtModeStatus,
		"id":         id,
		keyRequestID: id,
	}

	resp, _, err := c.sendArtRequest(ctx, req)
	if err != nil {
		c.logger.Debug("could not determine art mode, assuming safe to sync", "error", err)
		return true
	}

	isArt := resp.Value == "on"
	c.logger.Debug("art mode status", "value", resp.Value, "isArtMode", isArt)
	return isArt
}

// ListUploaded returns user-uploaded artwork on the TV (category MY-C0002 = "My Photos").
func (c *Client) ListUploaded(ctx context.Context) ([]ArtContent, error) {
	id := newRequestID()
	req := map[string]any{
		keyRequest:    keyGetContentList,
		"id":          id,
		keyRequestID:  id,
		"category_id": "MY-C0002",
	}

	resp, _, err := c.sendArtRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	contentListStr := resp.ContentList()
	if contentListStr == "" {
		return nil, nil
	}

	var items []ArtContent
	if err := json.Unmarshal([]byte(contentListStr), &items); err != nil {
		return nil, fmt.Errorf("parse content_list: %w", err)
	}

	filtered := make([]ArtContent, 0, len(items))
	for _, item := range items {
		if item.CategoryID == "MY-C0002" {
			filtered = append(filtered, item)
		}
	}

	return filtered, nil
}

// DeleteImages removes artwork from the TV by content IDs.
func (c *Client) DeleteImages(ctx context.Context, ids []string) error {
	id := newRequestID()

	contentIDList := make([]map[string]string, len(ids))
	for i, cid := range ids {
		contentIDList[i] = map[string]string{keyContentID: cid}
	}

	req := map[string]any{
		keyRequest:        "delete_image_list",
		"id":              id,
		keyRequestID:      id,
		"content_id_list": contentIDList,
	}

	_, _, err := c.sendArtRequest(ctx, req)
	return err
}

// SelectImage sets the currently displayed artwork by content ID.
func (c *Client) SelectImage(ctx context.Context, id string) error {
	reqID := newRequestID()

	req := map[string]any{
		keyRequest:   "select_image",
		"id":         reqID,
		keyRequestID: reqID,
		keyContentID: id,
		"show":       true,
	}

	_, _, err := c.sendArtRequest(ctx, req)
	return err
}

// getCategories retrieves the raw JSON list of artwork categories from the TV.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//
// Returns:
//   - json.RawMessage: The raw JSON byte slice containing the artwork categories.
//   - error: Any network, validation, or underlying TV API error encountered.
func (c *Client) getCategories(ctx context.Context) (json.RawMessage, error) {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "get_categories",
		"id":         id,
		keyRequestID: id,
	}

	_, raw, err := c.sendArtRequest(ctx, req)
	return raw, err
}

// registerImageAddedListener prepares a listener for the image_added event.
func (c *Client) registerImageAddedListener() func(ctx context.Context, timeout time.Duration) (string, error) {
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

// Upload sends an image to the TV via the art API and D2D socket transfer with
// the given matte style, returning the TV-assigned content ID. An empty matte
// falls back to the configured default.
func (c *Client) Upload(ctx context.Context, filePath, fileType, matte string) (string, error) {
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
	resp, raw, err := c.sendArtRequest(ctx, buildSendImageRequest(id, fileType, matte, stat.Size()))
	if err != nil {
		return "", err
	}

	c.logger.Debug("send_image raw response", "raw", string(raw))

	connInfoStr := resp.ConnInfo()
	if connInfoStr == "" {
		return "", fmt.Errorf("send_image: no conn_info in response")
	}

	c.logger.Debug("send_image conn_info string", "conn_info", connInfoStr)
	var ci connInfo
	if err := json.Unmarshal([]byte(connInfoStr), &ci); err != nil {
		return "", fmt.Errorf("parse conn_info: %w", err)
	}
	c.logger.Debug("send_image parsed conn_info", "ip", ci.IP, "port", ci.Port)

	// Step 2: Transfer the file over D2D socket.
	if err := uploadImageD2D(ctx, d2dUpload{
		info:          ci,
		filePath:      filePath,
		fileType:      fileType,
		timeout:       c.opts.ConnectionTimeout,
		skipTLSVerify: c.opts.SkipTLSVerify,
	}); err != nil {
		return "", fmt.Errorf("d2d transfer: %w", err)
	}

	// Step 3: Wait for the TV to confirm the image was added.
	contentID, err := waitForAdded(ctx, imageAddedTimeout)
	if err != nil {
		return "", fmt.Errorf("wait for confirmation: %w", err)
	}

	return contentID, nil
}

// buildSendImageRequest constructs the art-API "send_image" payload that
// announces a pending D2D socket transfer of the given size and matte.
func buildSendImageRequest(id, fileType, matte string, fileSize int64) map[string]any {
	// connectionIDModulus bounds the generated connection_id to 4 GiB, the
	// range the TV's D2D handshake expects.
	const connectionIDModulus = 4 * 1024 * 1024 * 1024

	return map[string]any{
		keyRequest:   "send_image",
		"file_type":  fileType,
		"id":         id,
		keyRequestID: id,
		"conn_info": map[string]any{
			"d2d_mode":      "socket",
			"connection_id": time.Now().UnixNano() % connectionIDModulus,
			"id":            id,
		},
		"image_date":        time.Now().Format("2006:01:02 15:04:05"),
		"matte_id":          matte,
		"portrait_matte_id": matte,
		"file_size":         fileSize,
	}
}

// sendArtRequest wraps req in the art-app envelope, sends it, and waits for the
// matching response, returning the parsed artResponse and the raw JSON payload.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//   - req: A map containing the structured payload for the art app API.
//
// Returns:
//   - *artResponse: The successfully parsed and structurally validated TV response.
//   - json.RawMessage: The raw JSON byte slice payload directly from the TV.
//   - error: Any network, validation, or underlying TV API error encountered.
func (c *Client) sendArtRequest(ctx context.Context, req map[string]any) (*artResponse, json.RawMessage, error) {
	name := fmt.Sprint(req[keyRequest])
	reqID := fmt.Sprint(req[keyRequestID])

	payload, err := artAppRequest(req)
	if err != nil {
		return nil, nil, fmt.Errorf("build %s request: %w", name, err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, reqID, c.opts.APITimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, nil, fmt.Errorf("parse %s response: %w", name, err)
	}

	if err := checkArtError(&resp); err != nil {
		return nil, nil, fmt.Errorf("%s error: %w", name, err)
	}

	return &resp, raw, nil
}
