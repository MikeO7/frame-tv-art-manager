package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const matteNone = "none"

// matteCatalog is the set of matte styles and colors a TV accepts. A matte ID
// is "none" or "<style>_<color>", for example "shadowbox_polar".
type matteCatalog struct {
	types  map[string]struct{}
	colors map[string]struct{}
}

// accepts reports whether the TV listed both parts of matte.
func (catalog matteCatalog) accepts(matte string) bool {
	if matte == matteNone {
		return true
	}
	style, color, found := strings.Cut(matte, "_")
	if !found {
		return false
	}
	_, knownStyle := catalog.types[style]
	_, knownColor := catalog.colors[color]
	return knownStyle && knownColor
}

// MatteCatalog reads get_matte_list once per connection.
func (t *protocolTransport) MatteCatalog(ctx context.Context) (matteCatalog, error) {
	conn, err := t.activeConnection()
	if err != nil {
		return matteCatalog{}, err
	}
	session := t.sessionFor(conn)
	if session.mattes != nil {
		return *session.mattes, nil
	}
	response, err := t.sendWithTimeout(ctx, conn, requestMatteList, nil, t.probeTimeout())
	if err != nil {
		return matteCatalog{}, err
	}
	catalog, err := parseMatteCatalog(response.MatteTypesRaw, response.MatteColorsRaw)
	if err != nil {
		return matteCatalog{}, err
	}
	session.mattes = &catalog
	return catalog, nil
}

func parseMatteCatalog(typesRaw, colorsRaw json.RawMessage) (matteCatalog, error) {
	var types []struct {
		Type string `json:"matte_type"`
	}
	var colors []struct {
		Color string `json:"color"`
	}
	typesJSON, colorsJSON := parsePolyString(typesRaw), parsePolyString(colorsRaw)
	if typesJSON == "" || colorsJSON == "" {
		return matteCatalog{}, errors.New("matte list omitted styles or colors")
	}
	if err := json.Unmarshal([]byte(typesJSON), &types); err != nil {
		return matteCatalog{}, fmt.Errorf("parse matte styles: %w", err)
	}
	if err := json.Unmarshal([]byte(colorsJSON), &colors); err != nil {
		return matteCatalog{}, fmt.Errorf("parse matte colors: %w", err)
	}
	catalog := matteCatalog{
		types: make(map[string]struct{}, len(types)), colors: make(map[string]struct{}, len(colors)),
	}
	for _, entry := range types {
		if value := strings.TrimSpace(entry.Type); value != "" {
			catalog.types[value] = struct{}{}
		}
	}
	for _, entry := range colors {
		if value := strings.TrimSpace(entry.Color); value != "" {
			catalog.colors[value] = struct{}{}
		}
	}
	if len(catalog.types) == 0 || len(catalog.colors) == 0 {
		return matteCatalog{}, errors.New("matte list is empty")
	}
	return catalog, nil
}
