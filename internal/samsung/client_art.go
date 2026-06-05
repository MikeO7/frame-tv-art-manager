package samsung

import (
	"context"
	"encoding/json"
	"fmt"
)

// isInArtMode checks if the TV is currently in art mode by querying
// the art API over the active WebSocket connection.
func (c *Client) isInArtMode(ctx context.Context) bool {
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

// getUploadedImages returns the list of user-uploaded images on the TV
// (category MY-C0002 = "My Photos").
func (c *Client) getUploadedImages(ctx context.Context) ([]ArtContent, error) {
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

// deleteImages removes artwork from the TV by content IDs.
func (c *Client) deleteImages(ctx context.Context, ids []string) error {
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

// selectImage sets the currently displayed artwork.
func (c *Client) selectImage(ctx context.Context, id string) error {
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
