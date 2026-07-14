// Package samsung provides types shared across the Samsung Frame TV client.
package samsung

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	EventD2DServiceMessage      = "d2d_service_message"
	EventD2DServiceMessageEvent = "d2d.service.message.event"
	EventChannelConnect         = "ms.channel.connect"
	EventChannelReady           = "ms.channel.ready"
)

const (
	methodRemoteControl = "ms.remote.control"
	stringTrue          = "true"
	stringFalse         = "false"
	stringOn            = "on"
	stringOff           = "off"
	jsonNull            = "null"
)

// DeviceInfo holds metadata about a connected Samsung TV, retrieved via
// the REST API at https://<ip>:8002/api/v2/.
type DeviceInfo struct {
	ModelName       string `json:"modelName"`
	FirmwareVersion string `json:"firmwareVersion"`
	FrameTVSupport  string `json:"FrameTVSupport"` // "true" or "false"
	PowerState      string `json:"PowerState"`     // "on" or "off"
}

// IsFrameTV returns true if the device reports Frame TV support.
func (d *DeviceInfo) IsFrameTV() bool {
	return d.FrameTVSupport == stringTrue
}

// IsOn returns true if the device is powered on.
func (d *DeviceInfo) IsOn() bool {
	return d.PowerState == stringOn
}

func validateFrameTVDevice(info DeviceInfo) error {
	if strings.TrimSpace(info.ModelName) == "" {
		return errors.New("device model is absent")
	}
	if strings.TrimSpace(info.FrameTVSupport) != stringTrue {
		return fmt.Errorf("device does not report Frame TV support: %w", ErrConnectionFailure)
	}
	switch strings.TrimSpace(info.PowerState) {
	case stringOn, stringOff:
		return nil
	default:
		return fmt.Errorf("unrecognized device power state %q", info.PowerState)
	}
}

// ArtContent represents a single artwork item on the TV, as returned by
// the get_content_list art API request.
type ArtContent struct {
	ContentID  string `json:"content_id"`
	CategoryID string `json:"category_id"`
}

// connInfo holds the D2D socket connection details returned by the TV
// when accepting an image upload request.
type connInfo struct {
	IP      string      `json:"ip"`
	Port    json.Number `json:"port"`
	Key     string      `json:"key"`
	Secured bool        `json:"secured"`
}

// deviceInfoResponse is the raw JSON envelope from GET /api/v2/.
type deviceInfoResponse struct {
	Device DeviceInfo `json:"device"`
}

// artResponse is the raw d2d.service.message.event data envelope.
type artResponse struct {
	Event     string `json:"event"`
	RequestID string `json:"request_id"`
	ID        string `json:"id"`
	ErrorCode int    `json:"error_code,omitempty"`

	// Fields vary by request type — parsed individually per command.
	// We use json.RawMessage because older Samsung TVs return these fields
	// as escaped JSON strings (e.g. "{\"ip\":\"...\"}"), while newer 2024 models
	// return them as raw JSON objects/arrays directly inside the payload.
	ContentListRaw json.RawMessage `json:"content_list,omitempty"`
	ContentID      string          `json:"content_id,omitempty"`
	Value          protocolString  `json:"value,omitempty"`
	Type           protocolString  `json:"type,omitempty"`
	ConnInfoRaw    json.RawMessage `json:"conn_info,omitempty"`
	Status         string          `json:"status,omitempty"`
	RequestData    string          `json:"request_data,omitempty"`
}

type protocolString string

func (s *protocolString) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		*s = protocolString(value)
		return nil
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte(jsonNull)) || raw[0] == '{' || raw[0] == '[' {
		return errors.New("protocol value is not a string or scalar")
	}
	*s = protocolString(raw)
	return nil
}

// ContentList safely unwraps the content_list field whether it is a string or an array.
func (a *artResponse) ContentList() string {
	return parsePolyString(a.ContentListRaw)
}

// ConnInfo safely unwraps the conn_info field whether it is a string or an object.
func (a *artResponse) ConnInfo() string {
	return parsePolyString(a.ConnInfoRaw)
}

// parsePolyString extracts a JSON string if the raw bytes are an escaped JSON string.
// If the bytes are already an object or array, it returns them as a raw JSON string.
func parsePolyString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == jsonNull {
		return ""
	}
	// It might be a regular JSON string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// It might be an object/array, return the raw JSON bytes as a string
	return string(raw)
}
