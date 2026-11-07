package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Art API generations differ in request names. Art API 2.x (2021 Frames)
// answers get_api_version, get_brightness, and *_auto_rotation_status; art API
// 4.x and newer (2022+) answers api_version, get_artmode_settings, and
// *_slideshow_status, and rejects the old names with an error or silence.
const (
	requestAPIVersion       = "api_version"
	requestLegacyAPIVersion = "get_api_version"
	requestArtModeSettings  = "get_artmode_settings"
	requestGetBrightness    = "get_brightness"
	requestCurrentArtwork   = "get_current_artwork"
	requestMatteList        = "get_matte_list"
	slideshowModernName     = "slideshow"
	slideshowLegacyName     = "auto_rotation"
	settingItemBrightness   = "brightness"
	defaultBrightnessMax    = 10
	// probeTimeout bounds requests that some generations ignore instead of
	// rejecting, so a fallback does not wait for the full API timeout.
	probeTimeout = 5 * time.Second
)

// artSession caches facts learned on one art-channel connection.
type artSession struct {
	conn          *connection
	apiVersion    string
	slideshowName string
	mattes        *matteCatalog
}

func (t *protocolTransport) sessionFor(conn *connection) *artSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.session == nil || t.session.conn != conn {
		t.session = &artSession{conn: conn}
	}
	return t.session
}

func (t *protocolTransport) probeTimeout() time.Duration {
	return min(t.config.RequestTimeout, probeTimeout)
}

// artAPIMajor returns the art API major version, probing it once per
// connection. It returns 0 when the TV answers neither version request.
func (t *protocolTransport) artAPIMajor(ctx context.Context, conn *connection) int {
	session := t.sessionFor(conn)
	if session.apiVersion == "" {
		for _, name := range []string{requestAPIVersion, requestLegacyAPIVersion} {
			response, err := t.sendWithTimeout(ctx, conn, name, nil, t.probeTimeout())
			if err == nil && strings.TrimSpace(response.Version) != "" {
				session.apiVersion = strings.TrimSpace(response.Version)
				break
			}
			if ctx.Err() != nil {
				return 0
			}
		}
	}
	major, err := strconv.Atoi(strings.SplitN(session.apiVersion, ".", 2)[0])
	if err != nil {
		return 0
	}
	return major
}

// Brightness reads the Art Mode brightness and the range the TV reports.
func (t *protocolTransport) Brightness(ctx context.Context) (BrightnessReading, error) {
	conn, err := t.activeConnection()
	if err != nil {
		return BrightnessReading{}, err
	}
	if major := t.artAPIMajor(ctx, conn); major == 0 || major >= 4 {
		response, settingsErr := t.sendWithTimeout(ctx, conn, requestArtModeSettings, nil, t.probeTimeout())
		if settingsErr == nil {
			return parseArtModeBrightness(response.DataRaw)
		}
		if major >= 4 && !errors.Is(settingsErr, ErrArtAPIError) {
			return BrightnessReading{}, settingsErr
		}
	}
	response, err := t.sendOnConnection(ctx, conn, requestGetBrightness, newRequestID(), nil)
	if err != nil {
		return BrightnessReading{}, err
	}
	return newBrightnessReading(string(response.Value), string(response.Min), string(response.Max))
}

// parseArtModeBrightness extracts the brightness item from the
// get_artmode_settings list, which arrives as an encoded JSON string.
func parseArtModeBrightness(raw json.RawMessage) (BrightnessReading, error) {
	data := parsePolyString(raw)
	if data == "" {
		return BrightnessReading{}, errors.New("artmode settings are absent")
	}
	var items []struct {
		Item  string         `json:"item"`
		Value protocolString `json:"value"`
		Min   protocolString `json:"min"`
		Max   protocolString `json:"max"`
	}
	if err := json.Unmarshal([]byte(data), &items); err != nil {
		return BrightnessReading{}, fmt.Errorf("parse artmode settings: %w", err)
	}
	for _, item := range items {
		if item.Item == settingItemBrightness {
			return newBrightnessReading(string(item.Value), string(item.Min), string(item.Max))
		}
	}
	return BrightnessReading{}, errors.New("artmode settings omit brightness")
}

func newBrightnessReading(value, minimum, maximum string) (BrightnessReading, error) {
	reading := BrightnessReading{Max: defaultBrightnessMax}
	var err error
	if reading.Value, err = strconv.Atoi(strings.TrimSpace(value)); err != nil {
		return BrightnessReading{}, fmt.Errorf("invalid brightness value %q", value)
	}
	if strings.TrimSpace(minimum) != "" {
		if reading.Min, err = strconv.Atoi(strings.TrimSpace(minimum)); err != nil {
			return BrightnessReading{}, fmt.Errorf("invalid brightness minimum %q", minimum)
		}
	}
	if strings.TrimSpace(maximum) != "" {
		if reading.Max, err = strconv.Atoi(strings.TrimSpace(maximum)); err != nil {
			return BrightnessReading{}, fmt.Errorf("invalid brightness maximum %q", maximum)
		}
	}
	if reading.Min < 0 || reading.Max > 100 || reading.Min >= reading.Max ||
		reading.Value < reading.Min || reading.Value > reading.Max {
		return BrightnessReading{}, fmt.Errorf("brightness %d is outside the reported range %d-%d",
			reading.Value, reading.Min, reading.Max)
	}
	return reading, nil
}

// ConfigureBrightness writes one Art Mode brightness level as the decimal
// string real TVs expect.
func (t *protocolTransport) ConfigureBrightness(ctx context.Context, value int) error {
	return t.mutate(ctx, "set_brightness", map[string]any{keyValue: strconv.Itoa(value)})
}

// Slideshow reads the slideshow under whichever request name this TV's art
// API generation answers.
func (t *protocolTransport) Slideshow(ctx context.Context) (SlideshowSetting, error) {
	conn, err := t.activeConnection()
	if err != nil {
		return SlideshowSetting{}, err
	}
	response, err := t.slideshowRequest(ctx, conn, "get", nil, false)
	if err != nil {
		return SlideshowSetting{}, err
	}
	return parseSlideshowSetting(string(response.Value), string(response.Type), response.CategoryID)
}

// ConfigureSlideshow writes a complete slideshow setting, keeping the
// category the TV already cycles through.
func (t *protocolTransport) ConfigureSlideshow(ctx context.Context, setting SlideshowSetting) error {
	if !setting.Valid() {
		return commandError(commandConfigureSlidesName, OutcomeNotAttempted, errors.New("invalid slideshow setting"))
	}
	conn, err := t.activeConnection()
	if err != nil {
		return commandError(commandConfigureSlidesName, OutcomeNotAttempted, err)
	}
	wireValue := strconv.Itoa(setting.Interval)
	if setting.Interval == 0 {
		wireValue = stringOff
	}
	_, err = t.slideshowRequest(ctx, conn, "set", map[string]any{
		keyValue: wireValue, keyCategoryID: setting.wireCategory(), "type": string(setting.Kind),
	}, true)
	return err
}

func (t *protocolTransport) slideshowRequest(
	ctx context.Context,
	conn *connection,
	action string,
	fields map[string]any,
	mutation bool,
) (*artResponse, error) {
	session := t.sessionFor(conn)
	if session.slideshowName == "" {
		session.slideshowName = slideshowModernName
		if major := t.artAPIMajor(ctx, conn); major > 0 && major < 4 {
			session.slideshowName = slideshowLegacyName
		}
	}
	names := []string{session.slideshowName, slideshowLegacyName}
	if session.slideshowName == slideshowLegacyName {
		names[1] = slideshowModernName
	}
	var lastErr error
	for index, name := range names {
		requestName := action + "_" + name + "_status"
		timeout := t.config.RequestTimeout
		if index == 0 && !mutation {
			timeout = t.probeTimeout()
		}
		var response *artResponse
		var err error
		if mutation {
			response, err = t.mutateWithTimeout(ctx, conn, requestName, fields, timeout)
		} else {
			response, err = t.sendWithTimeout(ctx, conn, requestName, fields, timeout)
		}
		if err == nil {
			session.slideshowName = name
			return response, nil
		}
		lastErr = err
		// A write that may have reached the TV is never retried under another
		// name; only an explicit rejection proves it was not applied.
		if ctx.Err() != nil || (mutation && !errors.Is(err, ErrArtAPIError)) ||
			(!mutation && !errors.Is(err, ErrArtAPIError) && !errors.Is(err, ErrTimeout)) {
			return nil, err
		}
	}
	return nil, lastErr
}

// CurrentArtwork reads the content ID the TV is displaying in Art Mode.
func (t *protocolTransport) CurrentArtwork(ctx context.Context) (string, error) {
	response, err := t.send(ctx, requestCurrentArtwork, nil)
	if err != nil {
		return "", err
	}
	contentID := strings.TrimSpace(response.ContentID)
	if contentID == "" {
		return "", errors.New("current artwork reply omitted content ID")
	}
	return contentID, nil
}
