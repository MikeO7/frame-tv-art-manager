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

// writeD2DPart writes data completely; bytes.Reader reports a short write as
// io.ErrShortWrite.
func writeD2DPart(writer io.Writer, data []byte, description string) error {
	if written, err := io.Copy(writer, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write %s after %d bytes: %w", description, written, err)
	}
	return nil
}

func uploadImageD2DFile(
	ctx context.Context,
	upload preparedUpload,
	info connInfo,
	timeout time.Duration,
	skipTLSVerify bool,
) error {
	if _, err := upload.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind image file: %w", err)
	}
	header := map[string]any{
		"num":        0,
		"total":      1,
		"fileLength": upload.size,
		"fileName":   "dummy",
		"fileType":   upload.fileType,
		"secKey":     info.Key,
		"version":    "0.0.1",
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("marshal d2d header: %w", err)
	}

	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialD2D(ctx, info, &dialer, skipTLSVerify)
	if err != nil {
		return fmt.Errorf("dial d2d socket: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// The write deadline covers the entire transfer.
	if err := conn.SetWriteDeadline(time.Now().Add(timeout + time.Duration(upload.size/d2dChunkSize)*100*time.Millisecond)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	// Cancellation interrupts a transfer in progress instead of waiting for the
	// write deadline. The outcome stays unknown because bytes may have arrived.
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stopCancel()

	// Send: [4-byte header length][header JSON][file bytes]
	headerLen := make([]byte, 4)
	binary.BigEndian.PutUint32(headerLen, uint32(len(headerJSON))) //nolint:gosec // JSON header length is small

	if err := writeD2DPart(conn, headerLen, "header length"); err != nil {
		return err
	}

	if err := writeD2DPart(conn, headerJSON, "header"); err != nil {
		return err
	}

	return streamFile(upload.file, conn, upload.size, upload.digest)
}
