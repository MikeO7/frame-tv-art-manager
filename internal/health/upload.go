package health

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

// defaultMaxUploadBytes caps an upload when MAX_DOWNLOAD_SIZE_MB is unset or
// invalid, guarding the endpoint against unbounded request bodies.
const defaultMaxUploadBytes = 20 << 20 // 20 MiB

const (
	fieldMessage  = "message"
	fieldFilename = "filename"
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
	if s.cfg.UploadToken != "" && !validUploadCredentials(r, s.cfg.UploadToken) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Frame TV artwork upload", charset="UTF-8"`)
		writeJSONError(w, http.StatusUnauthorized, "Authentication required")
		return
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
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return strings.EqualFold(parsed.Host, request.Host)
}

func validUploadCredentials(request *http.Request, token string) bool {
	username, password, ok := request.BasicAuth()
	return ok && username == "frame" && subtle.ConstantTimeCompare([]byte(password), []byte(token)) == 1
}

// processUpload reads, validates, and persists a single POSTed image, bounding
// the request body to mitigate denial-of-service via oversized payloads.
func (s *Server) processUpload(w http.ResponseWriter, r *http.Request) {
	select {
	case s.imports <- struct{}{}:
		defer func() { <-s.imports }()
	default:
		writeJSONError(w, http.StatusTooManyRequests, "Another artwork import is in progress")
		return
	}
	maxSize := s.maxUploadBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	fileData, err := parseUploadedFile(r, maxSize)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer func() { _ = fileData.Close() }()
	if s.importer == nil {
		s.logger.Error("artwork import rejected without authoritative collection")
		writeJSONError(w, http.StatusServiceUnavailable, "Artwork collection is unavailable")
		return
	}
	s.persistWithImporter(r.Context(), w, fileData)
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
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSONError(w, http.StatusBadRequest, "file too large (exceeds upload size limit)")
			return
		}
		if errors.Is(err, collection.ErrInvalidImport) {
			writeJSONError(w, http.StatusBadRequest, "Invalid or unsafe image")
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
