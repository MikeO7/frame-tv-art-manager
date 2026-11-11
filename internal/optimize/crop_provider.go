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

// newCropClient builds the HTTP client one pass calls the advanced crop
// provider with. Sharing it across the pass keeps its connections alive
// between pictures instead of paying a TCP and TLS handshake for each.
func newCropClient(cfg Config) *http.Client {
	dialTimeout := min(cropProviderTimeout(cfg), 10*time.Second)
	return &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: dialTimeout, KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   dialTimeout,
		ExpectContinueTimeout: time.Second,
	}}
}

func cropProviderTimeout(cfg Config) time.Duration {
	if cfg.SmartCropProviderTimeout <= 0 {
		return 8 * time.Second
	}
	return cfg.SmartCropProviderTimeout
}

func cropRectWithPolicy(ctx context.Context, src *image.RGBA, targetW, targetH int, env renderEnv) image.Rectangle {
	cfg, logger := env.cfg, env.logger
	targetAspect := float64(targetW) / float64(targetH)
	if cfg.SmartCropEnabled && cfg.SmartCropProvider == smartCropProviderHTTP {
		proposal, err := requestAdvancedCrop(ctx, src, targetW, targetH, env)
		upscale := 0.0
		if err == nil {
			upscale = fillScale(proposal.Width, proposal.Height, targetW, targetH)
		}
		tooSmall := cfg.MaxUpscale > 0 && upscale > cfg.MaxUpscale+1e-9
		if err == nil && proposal.Confidence >= cfg.SmartCropProviderMinConfidence && !tooSmall {
			box := image.Rect(proposal.X, proposal.Y, proposal.X+proposal.Width, proposal.Y+proposal.Height)
			return exactAspectWithin(box, targetAspect)
		}
		switch {
		case err == nil && tooSmall:
			logger.Info("advanced crop proposal needs too much upscaling; using local crop", "upscale", upscale)
		case err != nil:
			logger.Warn("advanced crop provider failed; using local crop", "error", err)
		default:
			logger.Info("advanced crop proposal below confidence threshold; using local crop", "confidence", proposal.Confidence)
		}
	}
	return cropRectForAspectWithProtection(
		src, targetAspect, cfg.SmartCropEnabled, cfg.SmartCropMinGain,
		cfg.SmartCropProtection, cfg.SmartCropProtectionStrength,
	)
}

func requestAdvancedCrop(ctx context.Context, src *image.RGBA, targetW, targetH int, env renderEnv) (cropProposal, error) {
	if env.cropClient == nil {
		return cropProposal{}, errors.New("advanced crop provider has no HTTP client")
	}
	endpoint, err := url.Parse(env.cfg.SmartCropProviderURL)
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
	requestCtx, cancel := context.WithTimeout(ctx, cropProviderTimeout(env.cfg))
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), bytes.NewReader(previewData.Bytes()))
	if err != nil {
		return cropProposal{}, fmt.Errorf("construct advanced crop request: %w", err)
	}
	request.Header.Set("Content-Type", "image/jpeg")
	response, err := env.cropClient.Do(request)
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

// exactAspectWithin shrinks the longer side of box about its centre to the
// target aspect, as the local crop does. A provider box may be up to 1% off,
// which would otherwise stretch the picture by tens of pixels at 4K.
func exactAspectWithin(box image.Rectangle, targetAspect float64) image.Rectangle {
	if float64(box.Dx())/float64(box.Dy()) > targetAspect {
		width := max(int(float64(box.Dy())*targetAspect), 1)
		left := box.Min.X + (box.Dx()-width)/2
		return image.Rect(left, box.Min.Y, left+width, box.Max.Y)
	}
	height := min(max(int(float64(box.Dx())/targetAspect), 1), box.Dy())
	top := box.Min.Y + (box.Dy()-height)/2
	return image.Rect(box.Min.X, top, box.Max.X, top+height)
}

func validateCropProposal(proposal cropProposal, bounds image.Rectangle, targetAspect float64) error {
	if proposal.Width <= 0 || proposal.Height <= 0 || proposal.X < 0 || proposal.Y < 0 ||
		proposal.Width > bounds.Dx() || proposal.Height > bounds.Dy() ||
		proposal.X > bounds.Dx()-proposal.Width || proposal.Y > bounds.Dy()-proposal.Height {
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
