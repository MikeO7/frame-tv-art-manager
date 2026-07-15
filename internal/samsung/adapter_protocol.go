package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

type protocolTransport struct {
	config              Config
	logger              *slog.Logger
	mu                  sync.Mutex
	conn                *connection
	deviceHTTPClient    *http.Client
	gateHTTPClient      *http.Client
	websocketHTTPClient *http.Client
}

func newProtocolTransport(config Config, dependencies Dependencies) observationTransport {
	logger := dependencies.Logger
	if logger == nil {
		logger = slog.Default()
	}
	transport := &protocolTransport{config: config, logger: logger.With("component", "samsung-tv")}
	transport.ensureHTTPClientsLocked()
	return transport
}

func (t *protocolTransport) Connect(ctx context.Context, dryRun bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureHTTPClientsLocked()
	host, port, err := observationAddress(t.config.Address)
	if err != nil {
		return err
	}
	if err := t.admitConnection(ctx, host, dryRun, t.gateHTTPClient); err != nil {
		return err
	}
	if t.conn != nil && t.conn.IsAlive() {
		return nil
	}
	persistToken := persistAuthenticationToken
	if dryRun {
		persistToken = func(context.Context, string, string) error { return nil }
	}
	t.conn = newConnection(connConfig{
		host:          host,
		port:          port,
		endpoint:      endpointArtApp,
		name:          t.config.ClientName,
		tokenFile:     t.config.TokenPath,
		timeout:       t.config.ConnectTimeout,
		skipTLSVerify: !t.config.VerifyTLS,
		logger:        t.logger,
		persistToken:  persistToken,
		httpClient:    t.websocketHTTPClient,
	})
	if err := t.conn.Open(ctx); err != nil {
		t.conn = nil
		return err
	}
	return nil
}

func (t *protocolTransport) admitConnection(
	ctx context.Context,
	host string,
	dryRun bool,
	gateClient *http.Client,
) error {
	if t.config.QuietGate {
		blocked, err := queryQuietGate(ctx, host, gateClient)
		if err != nil {
			return err
		}
		if blocked {
			return ErrGateFailed
		}
	}
	if !dryRun {
		return nil
	}
	_, err := loadAuthenticationToken(t.config.TokenPath)
	if err != nil {
		return fmt.Errorf("dry-run requires an existing token: %w", errors.Join(ErrUnauthorized, err))
	}
	return nil
}

func (t *protocolTransport) DeviceInfo(ctx context.Context) (DeviceInfo, error) {
	host, port, err := observationAddress(t.config.Address)
	if err != nil {
		return DeviceInfo{}, err
	}
	url := "https://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/api/v2/"
	client := t.cachedDeviceHTTPClient()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return DeviceInfo{}, fmt.Errorf("build device-info request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return DeviceInfo{}, fmt.Errorf("fetch device info: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return DeviceInfo{}, fmt.Errorf("device-info status %d: %w", response.StatusCode, ErrConnectionFailure)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDeviceInfoBytes+1))
	if err != nil {
		return DeviceInfo{}, fmt.Errorf("read device info: %w", err)
	}
	if len(body) > maxDeviceInfoBytes {
		return DeviceInfo{}, errors.New("device-info response exceeds limit")
	}
	var envelope deviceInfoResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return DeviceInfo{}, fmt.Errorf("parse device info: %w", err)
	}
	return envelope.Device, nil
}

func (t *protocolTransport) ArtMode(ctx context.Context) (string, error) {
	response, err := t.send(ctx, keyGetArtModeStatus, nil)
	if err != nil {
		return "", err
	}
	return string(response.Value), nil
}

func (t *protocolTransport) Inventory(ctx context.Context) (json.RawMessage, error) {
	response, err := t.send(ctx, keyGetContentList, map[string]any{keyCategoryID: userArtCategory})
	if err != nil {
		return nil, err
	}
	return response.ContentListRaw, nil
}

func (t *protocolTransport) Close(ctx context.Context) error {
	t.mu.Lock()
	conn := t.conn
	t.conn = nil
	deviceClient, gateClient, websocketClient := t.deviceHTTPClient, t.gateHTTPClient, t.websocketHTTPClient
	t.mu.Unlock()
	for _, client := range []*http.Client{deviceClient, gateClient, websocketClient} {
		if client != nil {
			client.CloseIdleConnections()
		}
	}
	if conn == nil {
		return nil
	}
	return conn.CloseContext(ctx)
}

func (t *protocolTransport) send(ctx context.Context, requestName string, fields map[string]any) (*artResponse, error) {
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil || !conn.IsAlive() {
		return nil, ErrNotConnected
	}
	return t.sendOnConnection(ctx, conn, requestName, newRequestID(), fields)
}

func (t *protocolTransport) sendOnConnection(
	ctx context.Context,
	conn *connection,
	requestName string,
	requestID string,
	fields map[string]any,
) (*artResponse, error) {
	request := map[string]any{
		keyRequest:   requestName,
		"id":         requestID,
		keyRequestID: requestID,
	}
	for key, value := range fields {
		request[key] = value
	}
	payload, err := artAppRequest(request)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", requestName, err)
	}
	raw, err := conn.SendAndWait(ctx, payload, requestID, t.config.RequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", requestName, err)
	}
	var response artResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("parse %s response: %w", requestName, err)
	}
	if err := checkArtError(&response); err != nil {
		return nil, fmt.Errorf("%s response: %w", requestName, err)
	}
	return &response, nil
}

func queryQuietGate(ctx context.Context, host string, client *http.Client) (bool, error) {
	if client == nil {
		return false, errors.New("quiet-gate HTTP client is unavailable")
	}
	url := "http://" + net.JoinHostPort(host, strconv.Itoa(portRESTGate)) + "/ms/art"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("build quiet-gate request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return false, fmt.Errorf("query quiet gate: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode != http.StatusOK, nil
}

func observationAddress(address string) (string, int, error) {
	if host, portText, err := net.SplitHostPort(address); err == nil {
		port, parseErr := strconv.Atoi(portText)
		if parseErr != nil || port < 1 || port > 65535 {
			return "", 0, fmt.Errorf("invalid Samsung address port %q", portText)
		}
		return host, port, nil
	}
	host := strings.TrimSpace(address)
	if host == "" {
		return "", 0, errors.New("samsung address is empty")
	}
	return host, portArtWSS, nil
}
