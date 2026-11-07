package samsung

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	eventImageAdded        = "image_added"
	eventImageDeleted      = "image_deleted"
	requestDeleteImageList = "delete_image_list"
)

// imageAddedMatch identifies this upload's image_added event. Art API 2.x
// echoes the upload's ID; art API 4.x sends none, so the event must name a
// content ID that was not on the TV before the upload. Events from other
// clients' uploads are skipped rather than bound to this artwork.
type imageAddedMatch struct {
	requestID string
	existing  map[string]struct{}
}

func waitForImageAdded(
	ctx context.Context,
	events <-chan json.RawMessage,
	match imageAddedMatch,
	timeout time.Duration,
) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case data, ok := <-events:
			if !ok {
				return "", ErrNotConnected
			}
			contentID, mine, err := parseImageAdded(data, match)
			if mine {
				return contentID, err
			}
		case <-timer.C:
			return "", fmt.Errorf("%w: waiting for image_added", ErrTimeout)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// parseImageAdded decodes one image_added event and reports whether it
// belongs to the upload described by match. An error for an event that is not
// ours explains why it was skipped and is not a failure of the upload.
func parseImageAdded(data json.RawMessage, match imageAddedMatch) (string, bool, error) {
	var response artResponse
	if err := json.Unmarshal(data, &response); err != nil {
		// A broadcast this client cannot decode is not evidence about the upload.
		return "", false, fmt.Errorf("decode image_added: %w", err)
	}
	contentID := strings.TrimSpace(response.ContentID)
	for _, correlation := range []string{response.RequestID, response.ID} {
		if correlation = strings.TrimSpace(correlation); correlation != "" {
			if correlation != match.requestID {
				return "", false, nil
			}
			if err := checkArtError(&response); err != nil {
				return "", true, fmt.Errorf("image_added error: %w", err)
			}
			return contentID, true, nil
		}
	}
	if err := checkArtError(&response); err != nil {
		// An uncorrelated failure cannot be attributed to this upload.
		return "", false, fmt.Errorf("uncorrelated image_added error: %w", err)
	}
	if _, existed := match.existing[contentID]; existed || contentID == "" {
		return "", false, nil
	}
	return contentID, true, nil
}

// sendImageFields builds the send_image request fields; sendOnConnection adds
// the request name and correlation IDs.
func sendImageFields(id string, upload preparedUpload) map[string]any {
	const connectionIDModulus = 4 * 1024 * 1024 * 1024
	portrait := upload.portraitMatte
	if portrait == "" {
		portrait = upload.matte
	}
	return map[string]any{
		"file_type": upload.fileType,
		"conn_info": map[string]any{
			"d2d_mode":      "socket",
			"connection_id": time.Now().UnixNano() % connectionIDModulus,
			"id":            id,
		},
		"image_date":        time.Now().Format("2006:01:02 15:04:05"),
		"matte_id":          upload.matte,
		"portrait_matte_id": portrait,
		"file_size":         upload.size,
	}
}
