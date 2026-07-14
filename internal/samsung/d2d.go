package samsung

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

const d2dChunkSize = 64 * 1024 // 64KB chunks for image transfer

// dialD2D opens the TCP (or TLS, when info.Secured) connection to the TV's
// Device-to-Device socket at info.IP:info.Port.
func dialD2D(ctx context.Context, info connInfo, dialer *net.Dialer, skipTLSVerify bool) (net.Conn, error) {
	addr := net.JoinHostPort(info.IP, string(info.Port))
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

func streamFile(f io.Reader, conn io.Writer, fileSize int64, expectedDigest [sha256.Size]byte) error {
	buf := make([]byte, d2dChunkSize)
	digest := sha256.New()
	totalWritten, err := io.CopyBuffer(io.MultiWriter(conn, digest), f, buf)
	if err != nil {
		return fmt.Errorf("stream image data after %d bytes: %w", totalWritten, err)
	}
	if totalWritten != fileSize {
		return fmt.Errorf("incomplete transfer: wrote %d of %d bytes", totalWritten, fileSize)
	}
	if !bytes.Equal(digest.Sum(nil), expectedDigest[:]) {
		return errors.New("transferred image digest does not match committed artwork")
	}
	return nil
}

func writeD2DPart(writer io.Writer, data []byte, description string) error {
	written, err := io.Copy(writer, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("write %s after %d bytes: %w", description, written, err)
	}
	if written != int64(len(data)) {
		return fmt.Errorf("write %s: %w", description, io.ErrShortWrite)
	}
	return nil
}

type preparedD2DUpload struct {
	file          *os.File
	fileSize      int64
	fileType      string
	info          connInfo
	timeout       time.Duration
	skipTLSVerify bool
	digest        [sha256.Size]byte
}

func uploadImageD2DFile(ctx context.Context, upload preparedD2DUpload) error {
	if _, err := upload.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind image file: %w", err)
	}
	// Build the D2D header.
	header := map[string]any{
		"num":        0,
		"total":      1,
		"fileLength": upload.fileSize,
		"fileName":   "dummy",
		"fileType":   upload.fileType,
		"secKey":     upload.info.Key,
		"version":    "0.0.1",
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("marshal d2d header: %w", err)
	}

	// Connect to the TV's D2D socket.
	dialer := net.Dialer{Timeout: upload.timeout}
	conn, err := dialD2D(ctx, upload.info, &dialer, upload.skipTLSVerify)
	if err != nil {
		return fmt.Errorf("dial d2d socket: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Set a write deadline for the entire transfer.
	if err := conn.SetWriteDeadline(time.Now().Add(upload.timeout + time.Duration(upload.fileSize/d2dChunkSize)*100*time.Millisecond)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}

	// Send: [4-byte header length][header JSON][file bytes]
	headerLen := make([]byte, 4)
	binary.BigEndian.PutUint32(headerLen, uint32(len(headerJSON))) //nolint:gosec // JSON header length is small

	if err := writeD2DPart(conn, headerLen, "header length"); err != nil {
		return err
	}

	if err := writeD2DPart(conn, headerJSON, "header"); err != nil {
		return err
	}

	return streamFile(upload.file, conn, upload.fileSize, upload.digest)
}
