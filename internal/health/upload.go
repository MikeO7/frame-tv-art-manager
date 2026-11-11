package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

const (
	// maxConcurrentSpools bounds how many uploads are received to temporary
	// files at once, and so the disk they can hold: this many times
	// MAX_DOWNLOAD_SIZE_MB.
	maxConcurrentSpools = 4
	fieldMessage        = "message"
	fieldFilename       = "filename"
	// uploadSpoolPattern names the private temporary file that holds an upload
	// while it is validated and imported.
	uploadSpoolPattern = ".frame-tv-upload-*.tmp"
)

// HandleUpload routes artwork upload requests: GET serves the upload UI and
// POST persists an image. All other methods and the disabled state short-circuit.
func (s *server) HandleUpload(w http.ResponseWriter, r *http.Request) {
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
	// Configuration validation requires UPLOAD_TOKEN whenever uploads are
	// enabled. Should that ever be bypassed, refuse rather than accept
	// unauthenticated writes.
	if s.cfg.UploadToken == "" {
		writeJSONError(w, http.StatusForbidden, "Upload endpoint requires UPLOAD_TOKEN")
		return
	}
	if result, wait := s.authenticate(r); result != authValid {
		writeAuthChallenge(w, result, wait)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(uploadHTML))
	case http.MethodPost:
		if !validUploadOrigin(r, s.cfg.HealthAllowedHosts) {
			writeJSONError(w, http.StatusForbidden, "Cross-origin uploads are not allowed")
			return
		}
		s.processUpload(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// validUploadOrigin accepts a request without an Origin header (curl, iOS
// Shortcuts) and a browser request whose Origin is the host it was sent to.
// Behind a reverse proxy that rewrites Host the two differ, so an Origin whose
// host name is listed in HEALTH_ALLOWED_HOSTS is accepted as well.
func validUploadOrigin(request *http.Request, allowedHosts []string) bool {
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
	return strings.EqualFold(parsed.Host, request.Host) || slices.Contains(allowedHosts, hostName(parsed.Host))
}

// processUpload spools a single POSTed image to a private temporary file and
// then imports it. Spooling keeps peak memory near one copy of the image, and
// the single import slot is taken only once the whole body has arrived, so a
// slow client never blocks other uploads while it sends.
func (s *server) processUpload(w http.ResponseWriter, r *http.Request) {
	select {
	case s.spools <- struct{}{}:
		defer func() { <-s.spools }()
	default:
		w.Header().Set("Retry-After", "5")
		writeJSONError(w, http.StatusTooManyRequests, "Too many uploads are in progress; try again shortly")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes(s.cfg))

	spooled, err := spoolUpload(r)
	if err != nil {
		var rejected uploadError
		if errors.As(err, &rejected) {
			writeJSONError(w, rejected.status, rejected.message)
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
	select {
	case s.imports <- struct{}{}:
		defer func() { <-s.imports }()
	case <-r.Context().Done():
		writeJSONError(w, http.StatusServiceUnavailable, "Upload was cancelled before it could be imported")
		return
	}
	s.persistWithImporter(r.Context(), w, spooled.file)
}

// MaxUploadBytes is the per-image size limit shared by web uploads and the
// MCP tools. Configuration validation keeps MAX_DOWNLOAD_SIZE_MB within
// 1..100, so the product cannot overflow or be non-positive.
func MaxUploadBytes(cfg *config.Config) int64 {
	return int64(cfg.MaxDownloadSizeMB) << 20
}

func (s *server) persistWithImporter(ctx context.Context, w http.ResponseWriter, reader io.Reader) {
	snapshot, err := s.importer.Import(ctx, collection.ImportRequest{
		Reader: reader, Hint: "upload", MaxBytes: MaxUploadBytes(s.cfg),
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

// uploadError is a client-caused rejection: the HTTP status to answer with
// and a message that is safe to return.
type uploadError struct {
	status  int
	message string
}

func (e uploadError) Error() string { return e.message }

func uploadTooLarge() uploadError {
	return uploadError{status: http.StatusRequestEntityTooLarge, message: "file too large (exceeds upload size limit)"}
}

func uploadEmpty() uploadError {
	return uploadError{status: http.StatusBadRequest, message: "no file was sent"}
}

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
	written, err := io.Copy(file, source)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			err = uploadTooLarge()
		} else {
			err = uploadError{status: http.StatusBadRequest, message: "upload interrupted or malformed"}
		}
		return spooledUpload{}, errors.Join(err, spooled.discard())
	}
	if written == 0 {
		return spooledUpload{}, errors.Join(uploadEmpty(), spooled.discard())
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
	invalid := uploadError{status: http.StatusBadRequest, message: "invalid multipart request"}
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, invalid
	}
	for parts := 0; ; parts++ {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			if parts == 0 {
				return nil, uploadEmpty()
			}
			return nil, uploadError{status: http.StatusBadRequest, message: "missing 'file' parameter"}
		}
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				return nil, uploadTooLarge()
			}
			return nil, invalid
		}
		if part.FormName() == "file" {
			return part, nil
		}
	}
}
