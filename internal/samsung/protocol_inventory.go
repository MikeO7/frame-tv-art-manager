package samsung

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// parseUploadedContent returns the user-uploaded artwork (category MY-C0002)
// from a get_content_list reply. TVs return every category in one list, and
// favorites (MY-C0004) or Art Store items (MY-C0008) repeat or add content IDs,
// so other categories are filtered out rather than treated as an anomaly.
func parseUploadedContent(raw json.RawMessage) ([]artContent, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == jsonNull {
		return nil, errors.New("content list is absent")
	}
	data := []byte(raw)
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		if strings.TrimSpace(encoded) == "" {
			return nil, errors.New("content list is blank")
		}
		data = []byte(encoded)
	}
	var content []artContent
	if err := json.Unmarshal(data, &content); err != nil {
		return nil, fmt.Errorf("parse content list: %w", err)
	}
	if content == nil {
		return nil, errors.New("content list is not an explicit array")
	}
	user := make([]artContent, 0, len(content))
	seen := make(map[string]struct{}, len(content))
	for _, item := range content {
		item.ContentID = strings.TrimSpace(item.ContentID)
		item.CategoryID = strings.TrimSpace(item.CategoryID)
		if item.CategoryID != userArtCategory {
			continue
		}
		if item.ContentID == "" {
			return nil, errors.New("content list contains a blank ID")
		}
		if _, duplicate := seen[item.ContentID]; duplicate {
			return nil, fmt.Errorf("content list contains duplicate ID %q", item.ContentID)
		}
		seen[item.ContentID] = struct{}{}
		user = append(user, item)
	}
	return user, nil
}
