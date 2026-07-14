package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
)

const headerAuthorization = "Authorization"

type providerJSONRequest struct {
	client      *http.Client
	url         string
	headers     map[string]string
	maxBytes    int64
	statusLabel string
	decodeLabel string
}

func fetchProviderJSON(ctx context.Context, request providerJSONRequest, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.url, nil)
	if err != nil {
		return providerRequestError(ctx, "create "+request.statusLabel, err)
	}
	for name, value := range request.headers {
		req.Header.Set(name, value)
	}

	resp, err := request.client.Do(req)
	if err != nil {
		return providerRequestError(ctx, "request "+request.statusLabel, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s error: %d", request.statusLabel, resp.StatusCode)
	}

	reader := http.MaxBytesReader(nil, resp.Body, request.maxBytes)
	if err := json.NewDecoder(reader).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", request.decodeLabel, err)
	}
	return nil
}

func providerRequestError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return fmt.Errorf("%s: %w", operation, contextErr)
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return fmt.Errorf("%s failed (network timeout=%t)", operation, networkErr.Timeout())
	}
	return fmt.Errorf("%s failed", operation)
}
