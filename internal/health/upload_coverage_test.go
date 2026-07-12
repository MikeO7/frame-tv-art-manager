package health

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type failingCopyAfterHeaderReader struct {
	data      []byte
	readCount int
	failAfter int
}

func (r *failingCopyAfterHeaderReader) Read(p []byte) (int, error) {
	r.readCount++
	if r.readCount > r.failAfter {
		return 0, errors.New("payload copy failure")
	}

	if len(r.data) == 0 {
		return 0, nil
	}

	rv := copy(p, r.data)
	r.data = r.data[rv:]
	return rv, nil
}

func TestReadImagePayload_CopyFailureIsHandled(t *testing.T) {
	data := bytes.Repeat([]byte{0xff, 0xd8, 0xff, 0xe0}, 300)
	reader := &failingCopyAfterHeaderReader{
		data:      data,
		failAfter: 1,
	}

	if _, _, uerr := readImagePayload(reader); uerr == nil || uerr.code != http.StatusInternalServerError {
		t.Fatalf("readImagePayload() = %v, want 500", uerr)
	}
}

func TestReadImagePayload_UnsupportedType(t *testing.T) {
	reader := strings.NewReader(strings.Repeat("not-an-image-", 100))
	if _, _, uerr := readImagePayload(reader); uerr == nil || uerr.code != http.StatusBadRequest {
		t.Fatalf("readImagePayload() = %v, want 400", uerr)
	}
}

func TestPersistImage_DeduplicatesExistingUpload(t *testing.T) {
	cfg := testConfig(0, true, t.TempDir())
	srv := NewServer(cfg, NewStatus(), silentLogger())

	payload := encodedTestImage(t, "jpeg")
	w1 := httptest.NewRecorder()
	srv.persistImage(w1, payload, ".jpg")
	if w1.Code != http.StatusOK {
		t.Fatalf("first persist status = %d, want %d: %s", w1.Code, http.StatusOK, w1.Body.String())
	}

	w2 := httptest.NewRecorder()
	srv.persistImage(w2, payload, ".jpg")
	if w2.Code != http.StatusOK {
		t.Fatalf("second persist status = %d, want %d: %s", w2.Code, http.StatusOK, w2.Body.String())
	}
	if got := w2.Body.String(); !strings.Contains(got, "already exists") {
		t.Fatalf("unexpected dedupe response = %q", got)
	}
}
