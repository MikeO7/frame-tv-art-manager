// Package samsung provides types shared across the Samsung Frame TV client.
package samsung

import "encoding/json"

const (
	EventD2DServiceMessage      = "d2d_service_message"
	EventD2DServiceMessageEvent = "d2d.service.message.event"
	EventChannelConnect         = "ms.channel.connect"
	EventChannelReady           = "ms.channel.ready"
)

const (
	stringTrue  = "true"
	stringFalse = "false"
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
	return d.PowerState == "on"
}

// ArtContent represents a single artwork item on the TV, as returned by
// the get_content_list art API request.
type ArtContent struct {
	ContentID  string `json:"content_id"`
	CategoryID string `json:"category_id"`
}

// ConnInfo holds the D2D socket connection details returned by the TV
// when accepting an image upload request.
type ConnInfo struct {
	IP      string      `json:"ip"`
	Port    json.Number `json:"port"`
	Key     string      `json:"key"`
	Secured bool        `json:"secured"`
}

// SlideshowStatus represents the slideshow configuration on the TV.
type SlideshowStatus struct {
	// Value is the slideshow interval in minutes, or "off".
	Value string `json:"value"`

	// Type is either "slideshow" or "shuffleslideshow".
	Type string `json:"type"`

	// CategoryID is the art category (e.g. "MY-C0002" for My Photos).
	CategoryID string `json:"category_id"`
}

// SendImageRequest holds the metadata for an image upload request.
type SendImageRequest struct {
	FilePath string
	FileType string // "jpg" or "png"
	FileSize int64
	Matte    string // matte style or "none"
}

// DeviceInfoResponse is the raw JSON envelope from GET /api/v2/.
type DeviceInfoResponse struct {
	Device DeviceInfo `json:"device"`
}

// ArtResponse is the raw d2d.service.message.event data envelope.
type ArtResponse struct {
	Event     string `json:"event"`
	RequestID string `json:"request_id"`
	ID        string `json:"id"`
	ErrorCode int    `json:"error_code,omitempty"`

	// Fields vary by request type — parsed individually per command.
	ContentList string `json:"content_list,omitempty"`
	ContentID   string `json:"content_id,omitempty"`
	Value       string `json:"value,omitempty"`
	ConnInfo    string `json:"conn_info,omitempty"`
	Status      string `json:"status,omitempty"`
	RequestData string `json:"request_data,omitempty"`
}
