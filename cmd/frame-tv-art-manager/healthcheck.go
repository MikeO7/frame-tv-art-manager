package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	defaultHealthPort     = 8080
	healthCheckTimeout    = 5 * time.Second
	healthCheckMaxBytes   = 1 << 20
	healthStatusHealthyOK = "ok"
)

func runHealthCheck(path string) {
	if err := performEndpointCheck(path); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	//nolint:forbidigo // CLI success output
	fmt.Println("Healthy")
	os.Exit(0)
}

func performEndpointCheck(path string) error {
	port := healthCheckPort()
	if port == 0 {
		// HEALTH_PORT=0 turns the server off, so there is nothing to probe.
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://"+net.JoinHostPort(healthCheckHost(), strconv.Itoa(port))+path,
		nil,
	)
	if err != nil {
		return fmt.Errorf("create health check request: %w", err)
	}
	response, err := (&http.Client{Timeout: healthCheckTimeout}).Do(request)
	if err != nil {
		return fmt.Errorf("health check request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed: HTTP status %d", response.StatusCode)
	}
	var status struct {
		Status string `json:"status"`
	}
	reader := http.MaxBytesReader(nil, response.Body, healthCheckMaxBytes)
	if err := json.NewDecoder(reader).Decode(&status); err != nil {
		return fmt.Errorf("decode health check response: %w", err)
	}
	if status.Status != healthStatusHealthyOK {
		return fmt.Errorf("health check returned status: %q", status.Status)
	}
	return nil
}

// healthCheckHost is the address the server listens on, or loopback when it
// listens on every interface.
func healthCheckHost() string {
	address := net.ParseIP(os.Getenv("HEALTH_BIND_ADDRESS"))
	switch {
	case address == nil:
		return "127.0.0.1"
	case address.IsUnspecified() && address.To4() == nil:
		return "::1"
	case address.IsUnspecified():
		return "127.0.0.1"
	default:
		return address.String()
	}
}

func healthCheckPort() int {
	if portText := os.Getenv("HEALTH_PORT"); portText != "" {
		if port, err := strconv.Atoi(portText); err == nil {
			return port
		}
	}
	return defaultHealthPort
}
