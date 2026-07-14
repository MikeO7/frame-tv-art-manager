package main

import (
	"context"
	"encoding/json"
	"fmt"
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

func runHealthCheck() {
	if err := performHealthCheck(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	//nolint:forbidigo // CLI success output
	fmt.Println("Healthy")
	os.Exit(0)
}

func performHealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/health", healthCheckPort()),
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

func healthCheckPort() int {
	if portText := os.Getenv("HEALTH_PORT"); portText != "" {
		if port, err := strconv.Atoi(portText); err == nil {
			return port
		}
	}
	return defaultHealthPort
}
