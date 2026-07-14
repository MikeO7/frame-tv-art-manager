package samsung

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"
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
