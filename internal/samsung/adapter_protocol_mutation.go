package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (t *protocolTransport) Upload(ctx context.Context, upload preparedUpload) (string, error) {
	conn, err := t.activeConnection()
	if err != nil {
		return "", commandError("upload", OutcomeNotAttempted, err)
	}
	added, cleanup, err := conn.registerPending(eventImageAdded)
	if err != nil {
		return "", commandError("upload", OutcomeNotAttempted, err)
	}
	defer cleanup()
	requestID := newRequestID()
	fields := sendImageFields(requestID, upload)
	response, err := t.mutateOnConnection(ctx, conn, requestSendImage, requestID, fields)
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
	if err := uploadImageD2DFile(ctx, upload, info, t.config.ConnectTimeout, !t.config.VerifyTLS); err != nil {
		return "", commandError("upload", OutcomeUnknown, fmt.Errorf("transfer upload: %w", err))
	}
	contentID, err := waitForImageAdded(ctx, added, imageAddedMatch{
		requestID: requestID, existing: upload.existing,
	}, t.config.RequestTimeout)
	if err != nil {
		return "", commandError("upload", mutationFailureOutcome(err), err)
	}
	if strings.TrimSpace(contentID) == "" {
		return "", commandError("upload", OutcomeUnknown, errors.New("upload confirmation omitted content ID"))
	}
	return strings.TrimSpace(contentID), nil
}

// Delete sends delete_image_list. Some TVs answer with an image_deleted event
// lacking the request ID, and others never answer, so a
// missing reply is not a failure: the adapter proves the outcome from a fresh
// inventory. Only an explicit error reply proves the delete was rejected.
func (t *protocolTransport) Delete(ctx context.Context, contentID string) error {
	conn, err := t.activeConnection()
	if err != nil {
		return commandError(commandDeleteName, OutcomeNotAttempted, err)
	}
	deleted, cleanup, err := conn.registerPending(eventImageDeleted)
	if err != nil {
		return commandError(commandDeleteName, OutcomeNotAttempted, err)
	}
	defer cleanup()
	requestID := newRequestID()
	replies, cleanupReply, err := conn.registerPending(requestID)
	if err != nil {
		return commandError(commandDeleteName, OutcomeNotAttempted, err)
	}
	defer cleanupReply()
	payload, err := artAppRequest(map[string]any{
		keyRequest: requestDeleteImageList, "id": requestID, keyRequestID: requestID,
		"content_id_list": []map[string]string{{keyContentID: contentID}},
	})
	if err != nil {
		return commandError(commandDeleteName, OutcomeNotAttempted, fmt.Errorf("build delete request: %w", err))
	}
	if err := conn.Send(ctx, payload); err != nil {
		return commandError(commandDeleteName, OutcomeUnknown, fmt.Errorf("send delete request: %w", err))
	}
	timer := time.NewTimer(t.probeTimeout())
	defer timer.Stop()
	select {
	case raw, ok := <-replies:
		return deleteReplyError(raw, ok)
	case raw, ok := <-deleted:
		return deleteReplyError(raw, ok)
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return commandError(commandDeleteName, OutcomeUnknown, ctx.Err())
	}
}

func deleteReplyError(raw json.RawMessage, ok bool) error {
	if !ok {
		return commandError(commandDeleteName, OutcomeUnknown, ErrNotConnected)
	}
	// image_deleted lists content IDs as an array, so only the error envelope
	// fields are decoded.
	var envelope struct {
		Event     string         `json:"event"`
		ErrorCode protocolString `json:"error_code"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return commandError(commandDeleteName, OutcomeUnknown, fmt.Errorf("parse delete reply: %w", err))
	}
	response := artResponse{Event: envelope.Event, ErrorCode: envelope.ErrorCode}
	if err := checkArtError(&response); err != nil {
		return commandError(commandDeleteName, OutcomeNotApplied, fmt.Errorf("delete reply: %w", err))
	}
	return nil
}

// Select shows one artwork; the adapter verifies it with get_current_artwork.
func (t *protocolTransport) Select(ctx context.Context, contentID string) error {
	return t.mutate(ctx, "select_image", map[string]any{
		keyContentID: contentID, keyCategoryID: userArtCategory, "show": true,
	})
}

func (t *protocolTransport) Wake(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return commandError(commandWakeName, OutcomeNotAttempted, err)
	}
	host, _, err := observationAddress(t.config.Address)
	if err != nil {
		return commandError(commandWakeName, OutcomeNotAttempted, err)
	}
	targets := wakeTargets(host, t.config.WakeBroadcast)
	if err := sendWakeOnLAN(ctx, t.config.MAC, targets, t.config.ConnectTimeout); err != nil {
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
		httpClient: t.websocketHTTPClient,
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
	_, err = t.mutateOnConnection(ctx, conn, name, newRequestID(), fields)
	return err
}

func (t *protocolTransport) mutateOnConnection(
	ctx context.Context,
	conn *connection,
	name string,
	requestID string,
	fields map[string]any,
) (*artResponse, error) {
	response, err := t.sendOnConnection(ctx, conn, name, requestID, fields)
	if err != nil {
		return nil, commandError(name, mutationFailureOutcome(err), err)
	}
	return response, nil
}

func (t *protocolTransport) mutateWithTimeout(
	ctx context.Context,
	conn *connection,
	name string,
	fields map[string]any,
	timeout time.Duration,
) (*artResponse, error) {
	response, err := t.sendWithTimeout(ctx, conn, name, fields, timeout)
	if err != nil {
		return nil, commandError(name, mutationFailureOutcome(err), err)
	}
	return response, nil
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
