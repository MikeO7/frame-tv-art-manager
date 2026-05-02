//nolint:goconst
package samsung

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// ArtAPI provides typed methods for Samsung Frame TV art channel operations.
// All communication happens over the WebSocket connection to the
// "com.samsung.art-app" endpoint.
const keyRequest = "request"
const keyRequestID = "request_id"

type ArtAPI struct {
	conn    *Connection
	timeout time.Duration
	logger  *slog.Logger
}

// NewArtAPI wraps an existing art-endpoint Connection with typed API methods.
func NewArtAPI(conn *Connection, timeout time.Duration, logger *slog.Logger) *ArtAPI {
	return &ArtAPI{
		conn:    conn,
		timeout: timeout,
		logger:  logger,
	}
}

// Connection returns the underlying WebSocket connection.
func (a *ArtAPI) Connection() *Connection {
	return a.conn
}

// GetContentList retrieves the list of artwork on the TV, optionally
// filtered by category. Use "MY-C0002" for user-uploaded photos.
func (a *ArtAPI) GetContentList(ctx context.Context, category string) ([]ArtContent, error) {
	id := NewRequestID()
	req := map[string]any{
		keyRequest:    "get_content_list",
		"id":         id,
		keyRequestID: id,
	}
	if category != "" {
		req["category_id"] = category
	}

	payload, err := ArtAppRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return nil, fmt.Errorf("get_content_list: %w", err)
	}

	var resp ArtResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if resp.ContentList == "" {
		return nil, nil
	}

	var items []ArtContent
	if err := json.Unmarshal([]byte(resp.ContentList), &items); err != nil {
		return nil, fmt.Errorf("parse content_list: %w", err)
	}

	// Filter by category if specified.
	if category != "" {
		filtered := make([]ArtContent, 0, len(items))
		for _, item := range items {
			if item.CategoryID == category {
				filtered = append(filtered, item)
			}
		}
		return filtered, nil
	}

	return items, nil
}

// SendImage sends an image upload request to the TV. The TV responds with
// D2D connection info that must be used to transfer the actual file bytes
// via UploadImageD2D.
func (a *ArtAPI) SendImage(ctx context.Context, req SendImageRequest) (*ConnInfo, error) {
	id := NewRequestID()

	artReq := map[string]any{
		keyRequest:    "send_image",
		"file_type":  req.FileType,
		"id":         id,
		keyRequestID: id,
		"conn_info": map[string]any{
			"d2d_mode":      "socket",
			"connection_id": time.Now().UnixNano() % (4 * 1024 * 1024 * 1024),
			"id":            id,
		},
		"image_date":        time.Now().Format("2006:01:02 15:04:05"),
		"matte_id":          req.Matte,
		"portrait_matte_id": req.Matte,
		"file_size":         req.FileSize,
	}

	payload, err := ArtAppRequest(artReq)
	if err != nil {
		return nil, fmt.Errorf("build send_image request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return nil, fmt.Errorf("send_image: %w", err)
	}

	a.logger.Debug("send_image raw response", "raw", string(raw))
	var resp ArtResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse send_image response: %w", err)
	}

	if resp.ConnInfo == "" {
		return nil, fmt.Errorf("send_image: no conn_info in response")
	}

	a.logger.Debug("send_image conn_info string", "conn_info", resp.ConnInfo)
	var connInfo ConnInfo
	if err := json.Unmarshal([]byte(resp.ConnInfo), &connInfo); err != nil {
		return nil, fmt.Errorf("parse conn_info: %w", err)
	}
	a.logger.Debug("send_image parsed conn_info", "ip", connInfo.IP, "port", connInfo.Port)

	return &connInfo, nil
}

// WaitForImageAdded blocks until the TV confirms the image was added,
// returning the assigned content_id.
func (a *ArtAPI) WaitForImageAdded(ctx context.Context, timeout time.Duration) (string, error) {
	// The TV sends an "image_added" event (not correlated to a request_id).
	raw, err := a.conn.SendAndWaitEvent(ctx, nil, "image_added", timeout)
	if err != nil {
		// If we sent nil payload, we just need to listen — re-register the
		// pending entry without sending anything.
		return "", fmt.Errorf("wait for image_added: %w", err)
	}

	var resp ArtResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parse image_added: %w", err)
	}

	return resp.ContentID, nil
}

// RegisterImageAddedListener registers a listener for the "image_added" event
// before sending the upload, so we don't miss the response.
func (a *ArtAPI) RegisterImageAddedListener() (waitFn func(ctx context.Context, timeout time.Duration) (string, error)) {
	ch := make(chan json.RawMessage, 1)

	a.conn.pendingMu.Lock()
	a.conn.pending["image_added"] = ch
	a.conn.pendingMu.Unlock()

	return func(ctx context.Context, timeout time.Duration) (string, error) {
		defer func() {
			a.conn.pendingMu.Lock()
			delete(a.conn.pending, "image_added")
			a.conn.pendingMu.Unlock()
		}()

		select {
		case data, ok := <-ch:
			if !ok {
				return "", ErrNotConnected
			}
			var resp ArtResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				return "", fmt.Errorf("parse image_added: %w", err)
			}
			return resp.ContentID, nil
		case <-time.After(timeout):
			return "", fmt.Errorf("%w: waiting for image_added", ErrTimeout)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// DeleteImages removes artwork from the TV by their content IDs.
func (a *ArtAPI) DeleteImages(ctx context.Context, contentIDs []string) error {
	id := NewRequestID()

	contentIDList := make([]map[string]string, len(contentIDs))
	for i, cid := range contentIDs {
		contentIDList[i] = map[string]string{"content_id": cid}
	}

	req := map[string]any{
		keyRequest:         "delete_image_list",
		"id":              id,
		keyRequestID:      id,
		"content_id_list": contentIDList,
	}

	payload, err := ArtAppRequest(req)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}

	_, err = a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return fmt.Errorf("delete_image_list: %w", err)
	}

	return nil
}

// SelectImage sets the currently displayed artwork on the TV.
func (a *ArtAPI) SelectImage(ctx context.Context, contentID string, show bool) error {
	id := NewRequestID()

	req := map[string]any{
		keyRequest:    "select_image",
		"id":         id,
		keyRequestID: id,
		"content_id": contentID,
		"show":       show,
	}

	payload, err := ArtAppRequest(req)
	if err != nil {
		return fmt.Errorf("build select_image request: %w", err)
	}

	_, err = a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return fmt.Errorf("select_image: %w", err)
	}

	return nil
}

// GetArtModeStatus returns "on" if the TV is in art mode, "off" otherwise.
func (a *ArtAPI) GetArtModeStatus(ctx context.Context) (string, error) {
	id := NewRequestID()

	req := map[string]any{
		keyRequest:    "get_artmode_status",
		"id":         id,
		keyRequestID: id,
	}

	payload, err := ArtAppRequest(req)
	if err != nil {
		return "", fmt.Errorf("build get_artmode_status request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return "", fmt.Errorf("get_artmode_status: %w", err)
	}

	var resp ArtResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parse artmode_status: %w", err)
	}

	return resp.Value, nil
}

// GetSlideshowStatus retrieves the current slideshow configuration.
func (a *ArtAPI) GetSlideshowStatus(ctx context.Context) (*SlideshowStatus, error) {
	id := NewRequestID()

	req := map[string]any{
		keyRequest:    "get_slideshow_status",
		"id":         id,
		keyRequestID: id,
	}

	payload, err := ArtAppRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build get_slideshow_status request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return nil, fmt.Errorf("get_slideshow_status: %w", err)
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

// SetSlideshowStatus updates the slideshow configuration on the TV.
func (a *ArtAPI) SetSlideshowStatus(ctx context.Context, s SlideshowStatus) error {
	id := NewRequestID()

	req := map[string]any{
		keyRequest:     "set_slideshow_status",
		"id":          id,
		keyRequestID:  id,
		"value":       s.Value,
		"category_id": s.CategoryID,
		"type":        s.Type,
	}

	payload, err := ArtAppRequest(req)
	if err != nil {
		return fmt.Errorf("build set_slideshow_status request: %w", err)
	}

	_, err = a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return fmt.Errorf("set_slideshow_status: %w", err)
	}

	return nil
}

// SetBrightness sets the art mode brightness (typically 0–10 or 0–50
// depending on TV model).
func (a *ArtAPI) SetBrightness(ctx context.Context, value int) error {
	id := NewRequestID()

	req := map[string]any{
		keyRequest:    "set_brightness",
		"id":         id,
		keyRequestID: id,
		"value":      value,
	}

	payload, err := ArtAppRequest(req)
	if err != nil {
		return fmt.Errorf("build set_brightness request: %w", err)
	}

	_, err = a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return fmt.Errorf("set_brightness: %w", err)
	}

	return nil
}

// GetCategories retrieves the list of all artwork categories available on the TV.
func (a *ArtAPI) GetCategories(ctx context.Context) (json.RawMessage, error) {
	id := NewRequestID()

	req := map[string]any{
		keyRequest:    "get_categories",
		"id":         id,
		keyRequestID: id,
	}

	payload, err := ArtAppRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build get_categories request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return nil, fmt.Errorf("get_categories: %w", err)
	}

	return raw, nil
}
