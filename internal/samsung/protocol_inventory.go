package samsung

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func parseUploadedContent(raw json.RawMessage) ([]ArtContent, error) {
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
	var content []ArtContent
	if err := json.Unmarshal(data, &content); err != nil {
		return nil, fmt.Errorf("parse content list: %w", err)
	}
	if content == nil {
		return nil, errors.New("content list is not an explicit array")
	}
	seen := make(map[string]struct{}, len(content))
	for index := range content {
		if err := normalizeUploadedItem(&content[index], seen); err != nil {
			return nil, err
		}
	}
	return content, nil
}

func normalizeUploadedItem(item *ArtContent, seen map[string]struct{}) error {
	item.ContentID = strings.TrimSpace(item.ContentID)
	item.CategoryID = strings.TrimSpace(item.CategoryID)
	if item.ContentID == "" {
		return errors.New("content list contains a blank ID")
	}
	if item.CategoryID != userArtCategory {
		return fmt.Errorf("content %q has unexpected category %q", item.ContentID, item.CategoryID)
	}
	if _, duplicate := seen[item.ContentID]; duplicate {
		return fmt.Errorf("content list contains duplicate ID %q", item.ContentID)
	}
	seen[item.ContentID] = struct{}{}
	return nil
}
