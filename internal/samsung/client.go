package samsung

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

// Client is the high-level facade for interacting with a single Samsung
// Frame TV. It composes the lower-level connection, art API, REST, gate,
// WoL, and remote control components into a clean interface that the
// sync engine consumes.
type Client struct {
	IP     string
	cfg    *config.Config
	logger *slog.Logger

	artConn *Connection
	artAPI  *ArtAPI
	info    *DeviceInfo
}

// NewClient creates a new TV client. Call Connect() to establish the
// WebSocket connection.
func NewClient(ip string, cfg *config.Config, logger *slog.Logger) *Client {
	return &Client{
		IP:     ip,
		cfg:    cfg,
		logger: logger.With("tv", ip),
	}
}

// Connect establishes a connection to the TV with the following sequence:
//  1. Wake-on-LAN (if MAC configured)
//  2. Silent REST Gate (if enabled) → abort if TV is not in art mode
//  3. Open WSS connection to art endpoint on port 8002
//  4. Fetch device info via REST API
func (c *Client) Connect(ctx context.Context) error {
	// Step 1: Wake-on-LAN.
	if c.cfg.TVMAC != "" {
		c.logger.Info("sending Wake-on-LAN", "mac", c.cfg.TVMAC)
		if err := SendWOL(c.cfg.TVMAC); err != nil {
			c.logger.Warn("WoL failed", "error", err)
		} else {
			// Brief pause to let the TV wake up.
			time.Sleep(2 * time.Second)
		}
	}

	// Step 2: Silent REST Gate.
	if c.cfg.EnableRESTGate {
		c.logger.Debug("checking REST gate")
		inArtMode, err := CheckArtModeGate(ctx, c.IP, c.cfg.GateTimeout)
		if err != nil {
			c.logger.Warn("REST gate error", "error", err)
		}
		if !inArtMode {
			c.logger.Info("REST gate: TV not in art mode, skipping to prevent popup")
			return ErrGateFailed
		}
		c.logger.Debug("REST gate: TV is in art mode")
	}

	// Step 3: Open WSS connection to art endpoint.
	tokenFile := c.tokenFilePath()
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0755); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}

	c.artConn = NewConnection(
		c.IP, 8002, "com.samsung.art-app",
		c.cfg.ClientName, tokenFile,
		c.cfg.ConnectionTimeout, c.logger,
	)

	if err := c.artConn.Open(ctx); err != nil {
		return fmt.Errorf("connect to art endpoint: %w", err)
	}

	c.artAPI = NewArtAPI(c.artConn, c.cfg.APITimeout, c.logger)

	// Step 4: Fetch device info.
	info, err := FetchDeviceInfo(ctx, c.IP, 8002, c.cfg.APITimeout)
	if err != nil {
		c.logger.Warn("could not fetch device info", "error", err)
		// Non-fatal — we can still sync without device info.
	} else {
		c.info = info
		c.logger.Info("connected",
			"model", info.ModelName,
			"firmware", info.FirmwareVersion,
			"frameTVSupport", info.FrameTVSupport,
		)
	}

	return nil
}

// Close shuts down the WebSocket connection.
func (c *Client) Close() error {
	if c.artConn != nil {
		return c.artConn.Close()
	}
	return nil
}

// IsInArtMode checks if the TV is currently in art mode by querying
// the art API over the active WebSocket connection.
func (c *Client) IsInArtMode(ctx context.Context) bool {
	// First check power state via REST.
	if c.info != nil && !c.info.IsOn() {
		c.logger.Debug("TV is powered off")
		return false
	}

	// Then check art mode via WebSocket.
	status, err := c.artAPI.GetArtModeStatus(ctx)
	if err != nil {
		c.logger.Debug("could not determine art mode, assuming safe to sync", "error", err)
		return true // backward-compatible: if we can't tell, try anyway
	}

	isArt := status == "on"
	c.logger.Debug("art mode status", "value", status, "isArtMode", isArt)
	return isArt
}

// GetUploadedImages returns the list of user-uploaded images on the TV
// (category MY-C0002 = "My Photos").
func (c *Client) GetUploadedImages(ctx context.Context) ([]ArtContent, error) {
	return c.artAPI.GetContentList(ctx, "MY-C0002")
}

// Upload sends an image to the TV via the art API + D2D socket transfer.
// Returns the content_id assigned by the TV.
func (c *Client) Upload(ctx context.Context, filePath, fileType string) (string, error) {
	stat, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", filePath, err)
	}

	matte := c.cfg.MatteStyle

	// Register the image_added listener BEFORE sending the upload request,
	// so we don't miss the response if it arrives quickly.
	waitForAdded := c.artAPI.RegisterImageAddedListener()

	// Step 1: Send the upload request to get D2D connection info.
	connInfo, err := c.artAPI.SendImage(ctx, SendImageRequest{
		FilePath: filePath,
		FileType: fileType,
		FileSize: stat.Size(),
		Matte:    matte,
	})
	if err != nil {
		return "", fmt.Errorf("send image request: %w", err)
	}

	// Step 2: Transfer the file over D2D socket.
	if err := UploadImageD2D(ctx, *connInfo, filePath, fileType, c.cfg.ConnectionTimeout); err != nil {
		return "", fmt.Errorf("d2d transfer: %w", err)
	}

	// Step 3: Wait for the TV to confirm the image was added.
	contentID, err := waitForAdded(ctx, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("wait for confirmation: %w", err)
	}

	return contentID, nil
}

// DeleteImages removes artwork from the TV by content IDs.
func (c *Client) DeleteImages(ctx context.Context, ids []string) error {
	return c.artAPI.DeleteImages(ctx, ids)
}

// SelectImage sets the currently displayed artwork.
func (c *Client) SelectImage(ctx context.Context, id string) error {
	return c.artAPI.SelectImage(ctx, id, true)
}

// SlideshowStatus returns the current slideshow configuration.
func (c *Client) SlideshowStatus(ctx context.Context) (*SlideshowStatus, error) {
	return c.artAPI.GetSlideshowStatus(ctx)
}

// SetSlideshow updates the slideshow configuration.
func (c *Client) SetSlideshow(ctx context.Context, s SlideshowStatus) error {
	return c.artAPI.SetSlideshowStatus(ctx, s)
}

// SetBrightness sets the art mode brightness.
func (c *Client) SetBrightness(ctx context.Context, val int) error {
	return c.artAPI.SetBrightness(ctx, val)
}

// TurnOff powers off the TV by holding KEY_POWER for 3 seconds via
// a separate remote control WebSocket connection.
func (c *Client) TurnOff(ctx context.Context) error {
	return TurnOffTV(
		ctx, c.IP, 8002,
		c.cfg.ClientName, c.tokenFilePath(),
		c.cfg.ConnectionTimeout, c.logger,
	)
}

// DeviceInfo returns the cached device info, or nil if not fetched.
func (c *Client) DeviceInfo() *DeviceInfo {
	return c.info
}

// tokenFilePath returns the path to the token file for this TV.
func (c *Client) tokenFilePath() string {
	safeIP := strings.ReplaceAll(c.IP, ".", "_")
	return filepath.Join(c.cfg.TokenDir, fmt.Sprintf("tv_%s.txt", safeIP))
}
