package health

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

// defaultMaxUploadBytes caps an upload when MAX_DOWNLOAD_SIZE_MB is unset or
// invalid, guarding the endpoint against unbounded request bodies.
const defaultMaxUploadBytes = 20 << 20 // 20 MiB

type atomicWriteOperations struct {
	createTemp func(string, string) (*os.File, error)
	chmod      func(*os.File, os.FileMode) error
	sync       func(*os.File) error
	close      func(*os.File) error
	remove     func(string) error
}

// HandleUpload routes artwork upload requests: GET serves the upload UI and
// POST persists an image. All other methods and the disabled state short-circuit.
func (s *Server) HandleUpload(w http.ResponseWriter, r *http.Request) {
	const uploadCSP = "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; " +
		"object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
	w.Header().Set("Content-Security-Policy", uploadCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	if s.cfg == nil || !s.cfg.UploadEnabled {
		writeJSONError(w, http.StatusForbidden, "Upload endpoint is disabled")
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(uploadHTML))
	case http.MethodPost:
		s.processUpload(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// processUpload reads, validates, and persists a single POSTed image, bounding
// the request body to mitigate denial-of-service via oversized payloads.
func (s *Server) processUpload(w http.ResponseWriter, r *http.Request) {
	maxSize := s.maxUploadBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	fileData, err := parseUploadedFile(r, maxSize)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer func() { _ = fileData.Close() }()

	payload, ext, uerr := readImagePayload(fileData)
	if uerr != nil {
		writeJSONError(w, uerr.code, uerr.msg)
		return
	}
	if err := validateUploadedImage(payload.Bytes()); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid or unsafe image: "+err.Error())
		return
	}

	s.persistImage(w, payload.Bytes(), ext)
}

func validateUploadedImage(payload []byte) error {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("decode header: %w", err)
	}
	const maxDimension = 16384
	const maxPixels = 80_000_000
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxDimension || cfg.Height > maxDimension ||
		int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return fmt.Errorf("dimensions %dx%d exceed limits", cfg.Width, cfg.Height)
	}
	if _, _, err := image.Decode(bytes.NewReader(payload)); err != nil {
		return fmt.Errorf("decode pixels: %w", err)
	}
	return nil
}

// maxUploadBytes resolves the configured per-upload limit, falling back to a
// safe default when the configured value is non-positive.
func (s *Server) maxUploadBytes() int64 {
	if mb := s.cfg.MaxDownloadSizeMB; mb > 0 {
		return int64(mb) << 20
	}
	return defaultMaxUploadBytes
}

// uploadError pairs an HTTP status code with a client-facing message so payload
// parsing can report precise failures without writing the response itself.
type uploadError struct {
	code int
	msg  string
}

func (e *uploadError) Error() string { return e.msg }

// readImagePayload buffers the upload, verifying via content sniffing that it is
// a supported image and that it stayed within the body-size limit.
func readImagePayload(r io.Reader) (*bytes.Buffer, string, *uploadError) {
	// io.ReadFull fills the 512-byte sniff buffer across multiple reads; a short
	// stream yields EOF/ErrUnexpectedEOF, expected for small images and not a failure.
	header := make([]byte, 512)
	n, err := io.ReadFull(r, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, "", readFailure(err, "Failed to read upload stream")
	}

	ext, ok := imageExtension(http.DetectContentType(header[:n]))
	if !ok {
		return nil, "", &uploadError{http.StatusBadRequest, "Unsupported file type (only JPEG and PNG are allowed)"}
	}

	body := bytes.NewBuffer(header[:n:n])
	if _, err := io.Copy(body, r); err != nil {
		return nil, "", readFailure(err, "Failed to read upload payload")
	}
	return body, ext, nil
}

// readFailure classifies a read error as either an oversized-body rejection
// (400) or an internal read failure (500) with the supplied message.
func readFailure(err error, internalMsg string) *uploadError {
	if isTooLarge(err) {
		return &uploadError{http.StatusBadRequest, "file too large (exceeds upload size limit)"}
	}
	return &uploadError{http.StatusInternalServerError, internalMsg}
}

// imageExtension maps a sniffed MIME type to a supported file extension.
func imageExtension(contentType string) (string, bool) {
	switch contentType {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	default:
		return "", false
	}
}

// persistImage writes the buffered payload under a content-addressed filename,
// short-circuiting with a deduplication response when the file already exists.
func (s *Server) persistImage(w http.ResponseWriter, payload []byte, ext string) {
	sum := sha256.Sum256(payload)
	filename := artwork.BuildHashName("upload", fmt.Sprintf("%x", sum)[:12], ext)
	destPath := filepath.Join(s.cfg.ArtworkDir, filename)

	if _, err := os.Stat(destPath); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			fieldStatus: statusOK,
			"message":   "File already exists (deduplicated)",
			"filename":  filename,
		})
		return
	}

	if err := atomicWriteArtwork(destPath, payload); err != nil {
		s.logger.Error("Failed to write uploaded file", "path", destPath, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to save uploaded file")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		fieldStatus: statusOK,
		"message":   "File uploaded successfully",
		"filename":  filename,
	})
}

func atomicWriteArtwork(path string, payload []byte) error {
	return atomicWriteArtworkWithOperations(path, payload, atomicWriteOperations{
		createTemp: os.CreateTemp,
		chmod:      func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) },
		sync:       func(file *os.File) error { return file.Sync() },
		close:      func(file *os.File) error { return file.Close() },
		remove:     os.Remove,
	})
}

func atomicWriteArtworkWithOperations(path string, payload []byte, operations atomicWriteOperations) error {
	tmp, err := operations.createTemp(filepath.Dir(path), ".upload-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary upload: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = operations.remove(tmpName) }()
	if err := operations.chmod(tmp, 0o644); err != nil {
		_ = operations.close(tmp)
		return fmt.Errorf("chmod temporary upload: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = operations.close(tmp)
		return fmt.Errorf("write temporary upload: %w", err)
	}
	if err := operations.sync(tmp); err != nil {
		_ = operations.close(tmp)
		return fmt.Errorf("sync temporary upload: %w", err)
	}
	if err := operations.close(tmp); err != nil {
		return fmt.Errorf("close temporary upload: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("commit upload: %w", err)
	}
	return nil
}

// isTooLarge reports whether err was caused by the request body exceeding the
// configured upload size limit (via http.MaxBytesReader).
func isTooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

// parseUploadedFile extracts the uploaded file payload from either a multipart form or raw request body.
func parseUploadedFile(r *http.Request, maxSize int64) (io.ReadCloser, error) {
	contentTypeHeader := r.Header.Get("Content-Type")
	if strings.Contains(contentTypeHeader, "multipart/form-data") {
		// r.Body is already wrapped with http.MaxBytesReader(maxSize) by the
		// caller, so form parsing is bounded despite gosec's G120 heuristic.
		err := r.ParseMultipartForm(maxSize) //nolint:gosec // body bounded upstream by MaxBytesReader
		if err != nil {
			return nil, errors.New("file too large or invalid request")
		}

		file, _, err := r.FormFile("file")
		if err != nil {
			return nil, errors.New("missing 'file' parameter")
		}
		return file, nil
	}
	return r.Body, nil
}
