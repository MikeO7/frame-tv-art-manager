package samsung

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type shortWriter struct {
	limit int
}

func (w shortWriter) Write(payload []byte) (int, error) {
	return min(len(payload), w.limit), nil
}

func TestStreamFileRejectsShortWrite(t *testing.T) {
	payload := bytes.Repeat([]byte("art"), 100)
	err := streamFile(bytes.NewReader(payload), shortWriter{limit: 7}, int64(len(payload)), sha256.Sum256(payload))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("streamFile() error = %v, want io.ErrShortWrite", err)
	}
}

func TestStreamFileRejectsBytesChangedAfterCommandPreparation(t *testing.T) {
	committed := []byte("committed")
	changed := []byte("tampered!")
	var output bytes.Buffer
	err := streamFile(bytes.NewReader(changed), &output, int64(len(changed)), sha256.Sum256(committed))
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("streamFile() error = %v, want digest mismatch", err)
	}
}

func TestWriteD2DPartRejectsShortWrite(t *testing.T) {
	err := writeD2DPart(shortWriter{limit: 2}, []byte("header"), "header")
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeD2DPart() error = %v, want io.ErrShortWrite", err)
	}
}

func TestUploadImageD2DFileStopsWhenCanceledMidTransfer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	defer func() {
		select {
		case conn := <-accepted:
			_ = conn.Close()
		default:
		}
	}()

	payload := bytes.Repeat([]byte{0xA5}, 64<<20)
	path := filepath.Join(t.TempDir(), "large.jpg")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	started := time.Now()
	err = uploadImageD2DFile(ctx, preparedD2DUpload{
		file: file, fileSize: int64(len(payload)), fileType: "jpg",
		info:    connInfo{IP: host, Port: json.Number(port), Key: "key"},
		timeout: time.Minute, digest: sha256.Sum256(payload),
	})
	if err == nil {
		t.Fatal("uploadImageD2DFile() succeeded although the receiver never read")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("uploadImageD2DFile() returned after %s; cancellation was ignored", elapsed)
	}
}
