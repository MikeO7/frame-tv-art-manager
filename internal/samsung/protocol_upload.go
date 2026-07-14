package samsung

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func waitForImageAdded(ctx context.Context, events <-chan json.RawMessage, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case data, ok := <-events:
		if !ok {
			return "", ErrNotConnected
		}
		return parseImageAdded(data)
	case <-timer.C:
		return "", fmt.Errorf("%w: waiting for image_added", ErrTimeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func parseImageAdded(data json.RawMessage) (string, error) {
	var response artResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("parse image_added: %w", err)
	}
	if err := checkArtError(&response); err != nil {
		return "", fmt.Errorf("image_added error: %w", err)
	}
	return response.ContentID, nil
}

func buildSendImageRequest(id, fileType, matte string, fileSize int64) map[string]any {
	const connectionIDModulus = 4 * 1024 * 1024 * 1024
	return map[string]any{
		keyRequest:   requestSendImage,
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
