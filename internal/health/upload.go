package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

// defaultMaxUploadBytes caps an upload when MAX_DOWNLOAD_SIZE_MB is unset or
// invalid, guarding the endpoint against unbounded request bodies.
const defaultMaxUploadBytes = 20 << 20 // 20 MiB

const (
	fieldMessage  = "message"
	fieldFilename = "filename"
	// uploadSpoolPattern names the private temporary file that holds an upload
	// while it is validated and imported.
	uploadSpoolPattern = ".frame-tv-upload-*.tmp"
)

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
	if s.cfg.DryRun {
		writeJSONError(w, http.StatusForbidden, "Upload endpoint is unavailable during dry-run")
		return
	}
	if s.cfg.UploadToken != "" {
		if result, wait := s.authenticate(r); result != authValid {
			writeAuthChallenge(w, result, wait)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(uploadHTML))
	case http.MethodPost:
		if !validUploadOrigin(r) {
			writeJSONError(w, http.StatusForbidden, "Cross-origin uploads are not allowed")
			return
		}
		s.processUpload(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func validUploadOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return len(request.Header.Values("Origin")) == 0
	}
	if len(origin) > 2048 || len(request.Header.Values("Origin")) != 1 {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if parsed.User != nil || parsed.Path != "" || parsed.ForceQuery || parsed.RawQuery != "" || strings.Contains(origin, "#") {
		return false
	}
	return strings.EqualFold(parsed.Host, request.Host)
}

// processUpload spools a single POSTed image to a private temporary file and
// then imports it. Spooling keeps peak memory near one copy of the image and
// keeps a slow client from holding the collection mutation while it sends.
func (s *Server) processUpload(w http.ResponseWriter, r *http.Request) {
	select {
	case s.imports <- struct{}{}:
		defer func() { <-s.imports }()
	default:
		writeJSONError(w, http.StatusTooManyRequests, "Another artwork import is in progress")
		return
	}
	if s.importer == nil {
		s.logger.Error("artwork import rejected without authoritative collection")
		writeJSONError(w, http.StatusServiceUnavailable, "Artwork collection is unavailable")
		return
	}
	maxSize := s.maxUploadBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	spooled, err := spoolUpload(r)
	if err != nil {
		var rejected uploadError
		if errors.As(err, &rejected) {
			writeJSONError(w, http.StatusBadRequest, rejected.message)
			return
		}
		s.logger.Error("artwork upload spooling failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to receive uploaded file")
		return
	}
	defer func() {
		if err := spooled.discard(); err != nil {
			s.logger.Warn("remove spooled upload", "error", err)
		}
	}()
	s.persistWithImporter(r.Context(), w, spooled.file)
}

// maxUploadBytes resolves the configured per-upload limit, falling back to a
// safe default when the configured value is non-positive.
func (s *Server) maxUploadBytes() int64 {
	if mb := s.cfg.MaxDownloadSizeMB; mb > 0 {
		return int64(mb) << 20
	}
	return defaultMaxUploadBytes
}

func (s *Server) persistWithImporter(ctx context.Context, w http.ResponseWriter, reader io.Reader) {
	snapshot, err := s.importer.Import(ctx, collection.ImportRequest{
		Reader: reader, Hint: "upload", MaxBytes: s.maxUploadBytes(),
	})
	if err != nil {
		if errors.Is(err, collection.ErrInvalidImport) {
			writeJSONError(w, http.StatusBadRequest, "Invalid or unsafe image")
			return
		}
		if errors.Is(err, collection.ErrCollectionFull) {
			writeJSONError(w, http.StatusConflict, "Artwork folder is full (MAX_ARTWORK_IMAGES reached)")
			return
		}
		s.logger.Error("artwork import failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to save uploaded file")
		return
	}
	if len(snapshot.Changes) == 1 && snapshot.Changes[0].Name != "" {
		message := "File imported successfully"
		if snapshot.Changes[0].Kind == collection.ChangeDuplicate {
			message = "File already exists (deduplicated)"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			fieldStatus: statusOK, fieldMessage: message, fieldFilename: snapshot.Changes[0].Name,
		})
		return
	}
	s.logger.Error("artwork import returned no matching committed item")
	writeJSONError(w, http.StatusInternalServerError, "Failed to verify uploaded file")
}

// uploadError is a client-caused rejection whose message is safe to return.
type uploadError struct{ message string }

func (e uploadError) Error() string { return e.message }

// spooledUpload is a received upload held in a private temporary file.
type spooledUpload struct{ file *os.File }

func (u spooledUpload) discard() error {
	return errors.Join(u.file.Close(), os.Remove(u.file.Name()))
}

// spoolUpload copies the image from a multipart "file" field or a raw body
// into a 0600 temporary file positioned at its start.
func spoolUpload(r *http.Request) (spooledUpload, error) {
	source, err := uploadPayload(r)
	if err != nil {
		return spooledUpload{}, err
	}
	file, err := os.CreateTemp("", uploadSpoolPattern)
	if err != nil {
		return spooledUpload{}, fmt.Errorf("create upload spool: %w", err)
	}
	spooled := spooledUpload{file: file}
	if _, err := io.Copy(file, source); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			err = uploadError{message: "file too large (exceeds upload size limit)"}
		} else {
			err = uploadError{message: "upload interrupted or malformed"}
		}
		return spooledUpload{}, errors.Join(err, spooled.discard())
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return spooledUpload{}, errors.Join(fmt.Errorf("rewind upload spool: %w", err), spooled.discard())
	}
	return spooled, nil
}

// uploadPayload returns a stream of the uploaded image without buffering it.
// For multipart requests it reads parts in order and stops at "file".
func uploadPayload(r *http.Request) (io.Reader, error) {
	if !strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		return r.Body, nil
	}
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, uploadError{message: "file too large or invalid request"}
	}
	for parts := 0; ; parts++ {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) && parts > 0 {
			return nil, uploadError{message: "missing 'file' parameter"}
		}
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				return nil, uploadError{message: "file too large (exceeds upload size limit)"}
			}
			return nil, uploadError{message: "file too large or invalid request"}
		}
		if part.FormName() == "file" {
			return part, nil
		}
	}
}
