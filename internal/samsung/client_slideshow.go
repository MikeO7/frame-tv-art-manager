package samsung

import (
	"context"
	"encoding/json"
	"fmt"
)

// slideshowStatus returns the current slideshow configuration.
func (c *Client) slideshowStatus(ctx context.Context) (*SlideshowStatus, error) {
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

// setSlideshow updates the slideshow configuration.
func (c *Client) setSlideshow(ctx context.Context, s SlideshowStatus) error {
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

// setBrightness sets the art mode brightness.
func (c *Client) setBrightness(ctx context.Context, val int) error {
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
