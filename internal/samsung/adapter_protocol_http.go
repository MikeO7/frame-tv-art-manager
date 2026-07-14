package samsung

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

func (t *protocolTransport) cachedDeviceHTTPClient() *http.Client {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureHTTPClientsLocked()
	return t.deviceHTTPClient
}

func (t *protocolTransport) cachedWebsocketHTTPClient() *http.Client {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureHTTPClientsLocked()
	return t.websocketHTTPClient
}

func (t *protocolTransport) ensureHTTPClientsLocked() {
	if t.deviceHTTPClient == nil {
		t.deviceHTTPClient = &http.Client{
			Timeout:   t.config.RequestTimeout,
			Transport: protocolHTTPTransport(t.config, true),
		}
	}
	if t.gateHTTPClient == nil {
		t.gateHTTPClient = &http.Client{
			Timeout:   t.config.GateTimeout,
			Transport: protocolHTTPTransport(t.config, false),
		}
	}
	if t.websocketHTTPClient == nil {
		t.websocketHTTPClient = &http.Client{Transport: protocolHTTPTransport(t.config, true)}
	}
}

func protocolHTTPTransport(config Config, secure bool) *http.Transport {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: config.ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: config.ConnectTimeout,
	}
	if secure {
		//nolint:gosec // Operators explicitly control verification for TV self-signed certificates.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: !config.VerifyTLS}
	}
	return transport
}
