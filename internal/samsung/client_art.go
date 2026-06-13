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

// IsInArtMode reports whether the TV is currently in art mode by querying
// the art API over the active WebSocket connection.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//
// Returns:
//   - bool: True if the TV is powered on and currently in art mode, false otherwise.
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

	payload, err := artAppRequest(req)
	if err != nil {
		c.logger.Debug("could not build get_artmode_status request, assuming safe to sync", "error", err)
		return true
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
	if err != nil {
		c.logger.Debug("could not determine art mode, assuming safe to sync", "error", err)
		return true
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.logger.Debug("parse artmode_status failed, assuming safe to sync", "error", err)
		return true
	}

	if err := checkArtError(&resp); err != nil {
		c.logger.Debug("get_artmode_status error response, assuming safe to sync", "error", err)
		return true
	}

	isArt := resp.Value == "on"
	c.logger.Debug("art mode status", "value", resp.Value, "isArtMode", isArt)
	return isArt
}

// ListUploaded returns user-uploaded artwork on the TV (category MY-C0002 = "My Photos").
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//
// Returns:
//   - []ArtContent: A list of user-uploaded artwork items currently stored on the TV.
//   - error: Any network or API error encountered during the fetch operation.
func (c *Client) ListUploaded(ctx context.Context) ([]ArtContent, error) {
	id := newRequestID()
	req := map[string]any{
		keyRequest:    keyGetContentList,
		"id":          id,
		keyRequestID:  id,
		"category_id": "MY-C0002",
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
	if err != nil {
		return nil, fmt.Errorf("get_content_list: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return nil, fmt.Errorf("get_content_list error: %w", err)
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
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//   - ids: A slice of content IDs representing the artwork to be deleted from the TV.
//
// Returns:
//   - error: Any network or API error encountered during the deletion.
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

	payload, err := artAppRequest(req)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
	if err != nil {
		return fmt.Errorf("delete_image_list: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parse delete_image_list response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return fmt.Errorf("delete_image_list error: %w", err)
	}

	return nil
}

// SelectImage sets the currently displayed artwork.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//   - id: The content ID of the artwork to be displayed on the TV screen.
//
// Returns:
//   - error: Any network or API error encountered during the selection.
func (c *Client) SelectImage(ctx context.Context, id string) error {
	reqID := newRequestID()

	req := map[string]any{
		keyRequest:   "select_image",
		"id":         reqID,
		keyRequestID: reqID,
		keyContentID: id,
		"show":       true,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return fmt.Errorf("build select_image request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, reqID, c.opts.APITimeout)
	if err != nil {
		return fmt.Errorf("select_image: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parse select_image response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return fmt.Errorf("select_image error: %w", err)
	}

	return nil
}

// getCategories retrieves the list of all artwork categories available on the TV.
func (c *Client) getCategories(ctx context.Context) (json.RawMessage, error) {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "get_categories",
		"id":         id,
		keyRequestID: id,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build get_categories request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
	if err != nil {
		return nil, fmt.Errorf("get_categories: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse get_categories response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return nil, fmt.Errorf("get_categories error: %w", err)
	}

	return raw, nil
}

// SaveMetadata fetches all available system information and artwork categories,
// saving them to a JSON file in the tokens directory for auditing.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//
// Returns:
//   - error: Any file I/O or network error encountered during export.
func (c *Client) SaveMetadata(ctx context.Context) error {
	metadata := make(map[string]any)
	metadata["timestamp"] = time.Now().Format(time.RFC3339)

	// 1. Basic Device Info.
	if c.info != nil {
		metadata["device"] = c.info
	}

	// 2. Slideshow Status.
	if ss, err := c.SlideshowStatus(ctx); err == nil {
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

// SlideshowStatus returns the current slideshow configuration.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//
// Returns:
//   - *SlideshowStatus: A struct detailing the current slideshow state on the TV.
//   - error: Any network or API error encountered during the fetch operation.
func (c *Client) SlideshowStatus(ctx context.Context) (*SlideshowStatus, error) {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "get_slideshow_status",
		"id":         id,
		keyRequestID: id,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build get_slideshow_status request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
	if err != nil {
		return nil, fmt.Errorf("get_slideshow_status: %w", err)
	}

	var artResp artResponse
	if err := json.Unmarshal(raw, &artResp); err != nil {
		return nil, fmt.Errorf("parse get_slideshow_status response: %w", err)
	}

	if err := checkArtError(&artResp); err != nil {
		return nil, fmt.Errorf("get_slideshow_status error: %w", err)
	}

	var resp struct {
		Value      string `json:"value"`
		Type       string `json:"type"`
		CategoryID string `json:"category_id"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse slideshow_status: %w", err)
	}

	return &SlideshowStatus{
		Value:      resp.Value,
		Type:       resp.Type,
		CategoryID: resp.CategoryID,
	}, nil
}

// SetSlideshow updates the slideshow configuration.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//   - s: The desired slideshow configuration to apply.
//
// Returns:
//   - error: Any network or API error encountered during the update.
func (c *Client) SetSlideshow(ctx context.Context, s SlideshowStatus) error {
	id := newRequestID()

	req := map[string]any{
		keyRequest:    "set_slideshow_status",
		"id":          id,
		keyRequestID:  id,
		"value":       s.Value,
		"category_id": s.CategoryID,
		"type":        s.Type,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return fmt.Errorf("build set_slideshow_status request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
	if err != nil {
		return fmt.Errorf("set_slideshow_status: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parse set_slideshow_status response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return fmt.Errorf("set_slideshow_status error: %w", err)
	}

	return nil
}

// SetBrightness sets the art mode brightness.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//   - val: The brightness value to set on the TV.
//
// Returns:
//   - error: Any network or API error encountered during the update.
func (c *Client) SetBrightness(ctx context.Context, val int) error {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "set_brightness",
		"id":         id,
		keyRequestID: id,
		"value":      val,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return fmt.Errorf("build set_brightness request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
	if err != nil {
		return fmt.Errorf("set_brightness: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parse set_brightness response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return fmt.Errorf("set_brightness error: %w", err)
	}

	return nil
}

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

// Upload sends an image to the TV via the art API + D2D socket transfer with the given matte style.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//   - filePath: The local filesystem path to the optimized image to upload.
//   - fileType: The MIME type or file extension format of the image.
//   - matte: The requested matte style ID string to apply to the artwork.
//
// Returns:
//   - string: The TV-assigned content ID for the newly uploaded artwork.
//   - error: Any network or API error encountered during the upload transfer.
//
//nolint:funlen // complexity justified for this domain-specific path
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
	if err := uploadImageD2D(ctx, ci, filePath, fileType, c.opts.ConnectionTimeout, c.opts.SkipTLSVerify); err != nil {
		return "", fmt.Errorf("d2d transfer: %w", err)
	}

	// Step 3: Wait for the TV to confirm the image was added.
	contentID, err := waitForAdded(ctx, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("wait for confirmation: %w", err)
	}

	return contentID, nil
}
