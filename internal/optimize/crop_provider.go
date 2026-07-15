package optimize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const advancedCropPreviewSize = 512

type cropProposal struct {
	X          int     `json:"x"`
	Y          int     `json:"y"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Confidence float64 `json:"confidence"`
}

//nolint:revive,nestif // crop inputs are orthogonal; provider decision deliberately falls back in one block
func cropRectWithPolicy(ctx context.Context, src *image.RGBA, targetW, targetH int, cfg Config, logger *slog.Logger) image.Rectangle {
	targetAspect := float64(targetW) / float64(targetH)
	if cfg.SmartCropEnabled && cfg.SmartCropProvider == smartCropProviderHTTP {
		proposal, err := requestAdvancedCrop(ctx, src, targetW, targetH, cfg)
		if err == nil && proposal.Confidence >= cfg.SmartCropProviderMinConfidence {
			return image.Rect(proposal.X, proposal.Y, proposal.X+proposal.Width, proposal.Y+proposal.Height)
		}
		if err != nil {
			logger.Warn("advanced crop provider failed; using local crop", "error", err)
		} else {
			logger.Info("advanced crop proposal below confidence threshold; using local crop", "confidence", proposal.Confidence)
		}
	}
	return cropRectForAspectWithProtection(
		src, targetAspect, cfg.SmartCropEnabled, cfg.SmartCropMinGain,
		cfg.SmartCropProtection, cfg.SmartCropProtectionStrength,
	)
}

//nolint:gocyclo,funlen // request construction, bounded transport, and response validation form one protocol transaction
func requestAdvancedCrop(ctx context.Context, src *image.RGBA, targetW, targetH int, cfg Config) (cropProposal, error) {
	endpoint, err := url.Parse(cfg.SmartCropProviderURL)
	if err != nil || (endpoint.Scheme != smartCropProviderHTTP && endpoint.Scheme != "https") || endpoint.Host == "" {
		return cropProposal{}, errors.New("advanced crop provider URL must be absolute HTTP(S)")
	}
	preview := resizePreview(src, advancedCropPreviewSize)
	var previewData bytes.Buffer
	if err := jpeg.Encode(&previewData, preview, &jpeg.Options{Quality: 85}); err != nil {
		return cropProposal{}, fmt.Errorf("encode crop preview: %w", err)
	}
	query := endpoint.Query()
	query.Set("source_width", strconv.Itoa(src.Bounds().Dx()))
	query.Set("source_height", strconv.Itoa(src.Bounds().Dy()))
	query.Set("target_width", strconv.Itoa(targetW))
	query.Set("target_height", strconv.Itoa(targetH))
	endpoint.RawQuery = query.Encode()
	timeout := cfg.SmartCropProviderTimeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), bytes.NewReader(previewData.Bytes()))
	if err != nil {
		return cropProposal{}, fmt.Errorf("construct advanced crop request: %w", err)
	}
	request.Header.Set("Content-Type", "image/jpeg")
	dialTimeout := min(timeout, 10*time.Second)
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: dialTimeout, KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   dialTimeout,
		ExpectContinueTimeout: time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Do(request)
	if err != nil {
		return cropProposal{}, fmt.Errorf("call advanced crop provider: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return cropProposal{}, fmt.Errorf("advanced crop provider returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return cropProposal{}, fmt.Errorf("read advanced crop response: %w", err)
	}
	if len(body) > 4096 {
		return cropProposal{}, errors.New("advanced crop provider response exceeds 4096-byte limit")
	}
	var proposal cropProposal
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proposal); err != nil {
		return cropProposal{}, fmt.Errorf("decode advanced crop response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return cropProposal{}, errors.New("advanced crop provider returned trailing JSON data")
	}
	if err := validateCropProposal(proposal, src.Bounds(), float64(targetW)/float64(targetH)); err != nil {
		return cropProposal{}, err
	}
	return proposal, nil
}

func validateCropProposal(proposal cropProposal, bounds image.Rectangle, targetAspect float64) error {
	if proposal.Width <= 0 || proposal.Height <= 0 || proposal.X < 0 || proposal.Y < 0 ||
		proposal.X+proposal.Width > bounds.Dx() || proposal.Y+proposal.Height > bounds.Dy() {
		return errors.New("advanced crop provider returned out-of-bounds crop")
	}
	if proposal.Confidence < 0 || proposal.Confidence > 1 {
		return errors.New("advanced crop provider returned invalid confidence")
	}
	if delta := math.Abs(float64(proposal.Width)/float64(proposal.Height)/targetAspect - 1); delta > 0.01 {
		return fmt.Errorf("advanced crop provider returned wrong aspect ratio: relative error %.3f", delta)
	}
	return nil
}

func resizePreview(src *image.RGBA, maximum int) image.Image {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if w <= maximum && h <= maximum {
		return src
	}
	if w >= h {
		return resizeCrop(src, src.Bounds(), maximum, max(1, h*maximum/w), true)
	}
	return resizeCrop(src, src.Bounds(), max(1, w*maximum/h), maximum, true)
}
