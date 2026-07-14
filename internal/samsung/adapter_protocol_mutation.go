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

func (t *protocolTransport) Upload(ctx context.Context, upload preparedUpload) (string, error) {
	conn, err := t.activeConnection()
	if err != nil {
		return "", commandError("upload", OutcomeNotAttempted, err)
	}
	added, cleanup, err := conn.registerPending("image_added")
	if err != nil {
		return "", commandError("upload", OutcomeNotAttempted, err)
	}
	defer cleanup()
	fields := buildSendImageRequest(newRequestID(), upload.fileType, upload.matte, upload.size)
	delete(fields, keyRequest)
	delete(fields, "id")
	delete(fields, keyRequestID)
	response, err := t.mutateOnConnection(ctx, conn, requestSendImage, fields)
	if err != nil {
		return "", err
	}
	var info connInfo
	if response.ConnInfo() == "" {
		return "", commandError("upload", OutcomeUnknown, errors.New("send_image response omitted connection info"))
	}
	if err := json.Unmarshal([]byte(response.ConnInfo()), &info); err != nil {
		return "", commandError("upload", OutcomeUnknown, fmt.Errorf("parse upload connection info: %w", err))
	}
	d2d := preparedD2DUpload{
		file: upload.file, fileSize: upload.size, fileType: upload.fileType, info: info,
		timeout: t.config.ConnectTimeout, skipTLSVerify: !t.config.VerifyTLS, digest: upload.digest,
	}
	if err := uploadImageD2DFile(ctx, d2d); err != nil {
		return "", commandError("upload", OutcomeUnknown, fmt.Errorf("transfer upload: %w", err))
	}
	contentID, err := waitForImageAdded(ctx, added, t.config.RequestTimeout)
	if err != nil {
		return "", commandError("upload", OutcomeUnknown, err)
	}
	if strings.TrimSpace(contentID) == "" {
		return "", commandError("upload", OutcomeUnknown, errors.New("upload confirmation omitted content ID"))
	}
	return strings.TrimSpace(contentID), nil
}

func (t *protocolTransport) Delete(ctx context.Context, contentID string) error {
	return t.mutate(ctx, "delete_image_list", map[string]any{
		"content_id_list": []map[string]string{{keyContentID: contentID}},
	})
}

func (t *protocolTransport) Select(ctx context.Context, contentID string) error {
	return t.mutate(ctx, "select_image", map[string]any{keyContentID: contentID, "show": true})
}

func (t *protocolTransport) Slideshow(ctx context.Context) (SlideshowSetting, error) {
	response, err := t.send(ctx, "get_slideshow_status", nil)
	if err != nil {
		return SlideshowSetting{}, err
	}
	return parseSlideshowSetting(string(response.Value), string(response.Type))
}

func (t *protocolTransport) ConfigureSlideshow(ctx context.Context, setting SlideshowSetting) error {
	if !setting.Valid() {
		return commandError(commandConfigureSlidesName, OutcomeNotAttempted, errors.New("invalid slideshow setting"))
	}
	wireValue := strconv.Itoa(setting.Interval)
	if setting.Interval == 0 {
		wireValue = stringOff
	}
	return t.mutate(ctx, requestSetSlideshowStatus, map[string]any{
		keyValue: wireValue, keyCategoryID: userArtCategory, "type": string(setting.Kind),
	})
}

func (t *protocolTransport) Brightness(ctx context.Context) (int, error) {
	response, err := t.send(ctx, "get_brightness", nil)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(response.Value)))
	if err != nil || value < 0 || value > 100 {
		return 0, fmt.Errorf("invalid brightness value %q", response.Value)
	}
	return value, nil
}

func (t *protocolTransport) ConfigureBrightness(ctx context.Context, value int) error {
	return t.mutate(ctx, "set_brightness", map[string]any{keyValue: value})
}

func (t *protocolTransport) Wake(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return commandError(commandWakeName, OutcomeNotAttempted, err)
	}
	if err := sendWakeOnLAN(ctx, t.config.MAC, t.config.ConnectTimeout); err != nil {
		return commandError(commandWakeName, OutcomeUnknown, err)
	}
	return nil
}

func (t *protocolTransport) PowerOff(ctx context.Context) error {
	conn, err := t.openRemoteControl(ctx)
	if err != nil {
		return commandError(commandPowerOffName, OutcomeNotAttempted, err)
	}
	defer func() { _ = conn.CloseContext(ctx) }()
	if err := sendPowerKey(ctx, conn, "Press"); err != nil {
		return commandError(commandPowerOffName, OutcomeUnknown, err)
	}
	timer := time.NewTimer(powerKeyHold)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return commandError(commandPowerOffName, OutcomeUnknown, ctx.Err())
	}
	if err := sendPowerKey(ctx, conn, "Release"); err != nil {
		return commandError(commandPowerOffName, OutcomeUnknown, err)
	}
	return nil
}

func (t *protocolTransport) openRemoteControl(ctx context.Context) (*connection, error) {
	host, port, err := observationAddress(t.config.Address)
	if err != nil {
		return nil, err
	}
	conn := newConnection(connConfig{
		host: host, port: port, endpoint: endpointRemoteControl, name: t.config.ClientName,
		tokenFile: t.config.TokenPath, timeout: t.config.ConnectTimeout,
		skipTLSVerify: !t.config.VerifyTLS, logger: t.logger,
		httpClient: t.cachedWebsocketHTTPClient(),
	})
	if err := conn.Open(ctx); err != nil {
		return nil, fmt.Errorf("open remote control: %w", err)
	}
	return conn, nil
}

func (t *protocolTransport) mutate(ctx context.Context, name string, fields map[string]any) error {
	conn, err := t.activeConnection()
	if err != nil {
		return commandError(name, OutcomeNotAttempted, err)
	}
	_, err = t.mutateOnConnection(ctx, conn, name, fields)
	return err
}

func (t *protocolTransport) mutateOnConnection(
	ctx context.Context,
	conn *connection,
	name string,
	fields map[string]any,
) (*artResponse, error) {
	response, err := t.sendOnConnection(ctx, conn, name, fields)
	if err == nil {
		return response, nil
	}
	if errors.Is(err, ErrArtAPIError) || errors.Is(err, ErrStorageFull) {
		return nil, commandError(name, OutcomeNotApplied, err)
	}
	return nil, commandError(name, OutcomeUnknown, err)
}

func (t *protocolTransport) activeConnection() (*connection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == nil || !t.conn.IsAlive() {
		return nil, ErrNotConnected
	}
	return t.conn, nil
}

func sendPowerKey(ctx context.Context, conn *connection, command string) error {
	payload, err := json.Marshal(map[string]any{
		keyMethod: methodRemoteControl,
		keyParams: map[string]any{
			keyRemoteCommand: command, keyRemoteData: remotePowerKey,
			keyRemoteOption: stringFalse, keyRemoteType: remoteSendKey,
		},
	})
	if err != nil {
		return fmt.Errorf("encode power %s: %w", strings.ToLower(command), err)
	}
	if err := conn.Send(ctx, payload); err != nil {
		return fmt.Errorf("send power %s: %w", strings.ToLower(command), err)
	}
	return nil
}
