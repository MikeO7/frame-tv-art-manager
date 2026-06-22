package samsung

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

const d2dChunkSize = 64 * 1024 // 64KB chunks for image transfer

// dialD2D opens the TCP (or TLS, when info.Secured) connection to the TV's
// Device-to-Device socket at info.IP:info.Port.
func dialD2D(ctx context.Context, info connInfo, dialer *net.Dialer, skipTLSVerify bool) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%s", info.IP, info.Port)
	if info.Secured {
		tlsConf := &tls.Config{InsecureSkipVerify: skipTLSVerify} //nolint:gosec // Samsung self-signed cert
		tlsDialer := &tls.Dialer{
			NetDialer: dialer,
			Config:    tlsConf,
		}
		return tlsDialer.DialContext(ctx, "tcp", addr)
	}
	return dialer.DialContext(ctx, "tcp", addr)
}

func streamFile(f io.Reader, conn io.Writer, fileSize int64) error {
	buf := make([]byte, d2dChunkSize)
	var totalWritten int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			if _, writeErr := conn.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("write image data at offset %d: %w", totalWritten, writeErr)
			}
			totalWritten += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read image file: %w", readErr)
		}
	}

	if totalWritten != fileSize {
		return fmt.Errorf("incomplete transfer: wrote %d of %d bytes", totalWritten, fileSize)
	}
	return nil
}

// d2dUpload groups the non-context parameters for uploadImageD2D.
type d2dUpload struct {
	info          connInfo
	filePath      string
	fileType      string
	timeout       time.Duration
	skipTLSVerify bool
}

// uploadImageD2D transfers an image file to the TV via a direct TCP/TLS
// socket connection — the "Device-to-Device" transfer protocol used by
// Samsung Frame TVs for high-resolution image uploads.
//
// Protocol:
//  1. Connect to info.IP:info.Port (TLS if info.Secured)
//  2. Send 4-byte big-endian header length
//  3. Send JSON header with file metadata and security key
//  4. Send raw image bytes in 64KB chunks
//  5. Close socket
//
// The caller must separately wait for the "image_added" event on the
// WebSocket to confirm the upload succeeded and get the content_id.
func uploadImageD2D(ctx context.Context, up d2dUpload) error {
	// Open the image file.
	f, err := os.Open(filepath.Clean(up.filePath))
	if err != nil {
		return fmt.Errorf("open image file: %w", err)
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat image file: %w", err)
	}
	fileSize := stat.Size()

	// Build the D2D header.
	header := map[string]any{
		"num":        0,
		"total":      1,
		"fileLength": fileSize,
		"fileName":   "dummy",
		"fileType":   up.fileType,
		"secKey":     up.info.Key,
		"version":    "0.0.1",
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("marshal d2d header: %w", err)
	}

	// Connect to the TV's D2D socket.
	dialer := net.Dialer{Timeout: up.timeout}
	conn, err := dialD2D(ctx, up.info, &dialer, up.skipTLSVerify)
	if err != nil {
		return fmt.Errorf("dial d2d socket %s:%s: %w", up.info.IP, up.info.Port, err)
	}
	defer func() { _ = conn.Close() }()

	// Set a write deadline for the entire transfer.
	if err := conn.SetWriteDeadline(time.Now().Add(up.timeout + time.Duration(fileSize/d2dChunkSize)*100*time.Millisecond)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}

	// Send: [4-byte header length][header JSON][file bytes]
	headerLen := make([]byte, 4)
	binary.BigEndian.PutUint32(headerLen, uint32(len(headerJSON))) //nolint:gosec // JSON header length is small

	if _, err := conn.Write(headerLen); err != nil {
		return fmt.Errorf("write header length: %w", err)
	}

	if _, err := conn.Write(headerJSON); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	return streamFile(f, conn, fileSize)
}
