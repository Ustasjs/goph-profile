package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/ustasjs/goph-profile/internal/avatar"
	"github.com/ustasjs/goph-profile/internal/metrics"
	"github.com/ustasjs/goph-profile/internal/server/service"
)

// maxUploadBytes limits one avatar file, per the spec.
const maxUploadBytes = 10 << 20

// maxUserIDLen matches the user_id VARCHAR(255) column: a longer
// header must read as a bad request, not as a database error.
const maxUserIDLen = 255

// requireUserID reads the X-User-ID header and answers 400 itself
// when the header is missing or does not fit the schema.
func requireUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "X-User-ID header is required")
		return "", false
	}
	// VARCHAR(n) counts characters, not bytes.
	if utf8.RuneCountInString(userID) > maxUserIDLen {
		writeError(w, http.StatusBadRequest, "X-User-ID is too long")
		return "", false
	}
	return userID, true
}

// AvatarService is what the handlers need from the service layer.
type AvatarService interface {
	Upload(ctx context.Context, userID, fileName string, data []byte) (avatar.Avatar, error)
	Metadata(ctx context.Context, id string) (avatar.Avatar, error)
	List(ctx context.Context, userID string) ([]avatar.Avatar, error)
	GetFile(ctx context.Context, id string) (service.File, error)
	LatestFile(ctx context.Context, userID string) (service.File, error)
	ThumbnailFile(ctx context.Context, id, size string) (service.File, error)
	Delete(ctx context.Context, id, requesterID string) error
	DeleteLatest(ctx context.Context, userID, requesterID string) error
}

type handlers struct {
	svc AvatarService
	m   *metrics.Server
	log *slog.Logger
}

// uploadResponse is the 201 body of POST /api/v1/avatars.
type uploadResponse struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	URL    string `json:"url"`
	// Status is the public processing state; right after the upload
	// it is always "processing".
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func (h *handlers) upload(w http.ResponseWriter, r *http.Request) {
	// Every exit before the service call is a client mistake, so the
	// rejected outcome is the default and success flips it at the end.
	status := metrics.StatusRejected
	start := time.Now()
	defer func() { h.m.ObserveUpload(status, time.Since(start).Seconds()) }()

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// Cheap reject when the client declares the size; the reader
	// below still guards against undeclared bodies.
	if r.ContentLength > maxUploadBytes {
		writeTooLarge(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)

	file, header, err := formFile(r)
	if err != nil {
		if isTooLarge(err) {
			writeTooLarge(w)
			return
		}
		writeError(w, http.StatusBadRequest, `multipart field "file" or "image" is required`)
		return
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		if isTooLarge(err) {
			writeTooLarge(w)
			return
		}
		writeError(w, http.StatusBadRequest, "read uploaded file")
		return
	}

	a, err := h.svc.Upload(r.Context(), userID, header.Filename, data)
	if err != nil {
		status = metrics.StatusError
		h.serviceError(r.Context(), w, err)
		return
	}

	status = metrics.StatusOK
	writeJSON(w, http.StatusCreated, uploadResponse{
		ID:        a.ID,
		UserID:    a.UserID,
		URL:       originalURL(a.ID),
		Status:    "processing",
		CreatedAt: a.CreatedAt,
	})
}

// formFile accepts the spec's field name and the one the bundled
// SPA actually sends.
func formFile(r *http.Request) (multipart.File, *multipart.FileHeader, error) {
	file, header, err := r.FormFile("file")
	if err == nil {
		return file, header, nil
	}
	if isTooLarge(err) {
		return nil, nil, err
	}
	return r.FormFile("image")
}

func (h *handlers) getFile(w http.ResponseWriter, r *http.Request) {
	f, err := h.svc.GetFile(r.Context(), chi.URLParam(r, "avatarID"))
	if err != nil {
		h.serviceError(r.Context(), w, err)
		return
	}
	h.streamFile(r.Context(), w, f)
}

func (h *handlers) latestFile(w http.ResponseWriter, r *http.Request) {
	f, err := h.svc.LatestFile(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		h.serviceError(r.Context(), w, err)
		return
	}
	h.streamFile(r.Context(), w, f)
}

func (h *handlers) getThumbnail(w http.ResponseWriter, r *http.Request) {
	f, err := h.svc.ThumbnailFile(r.Context(), chi.URLParam(r, "avatarID"), chi.URLParam(r, "size"))
	if err != nil {
		h.serviceError(r.Context(), w, err)
		return
	}
	h.streamFile(r.Context(), w, f)
}

func (h *handlers) streamFile(ctx context.Context, w http.ResponseWriter, f service.File) {
	defer func() { _ = f.Body.Close() }()
	w.Header().Set("Content-Type", f.ContentType)
	if f.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	}
	if _, err := io.Copy(w, f.Body); err != nil {
		// Headers are gone; nothing to answer. Usually the client
		// hung up mid-download.
		h.log.DebugContext(ctx, "stream avatar", "error", err)
	}
}

// metadataResponse is the body of GET .../metadata and the list items.
type metadataResponse struct {
	ID         string      `json:"id"`
	UserID     string      `json:"user_id"`
	FileName   string      `json:"file_name"`
	MimeType   string      `json:"mime_type"`
	Size       int64       `json:"size"`
	Dimensions dimensions  `json:"dimensions"`
	Thumbnails []thumbnail `json:"thumbnails"`
	URL        string      `json:"url"`
	Status     string      `json:"status"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
}

type dimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type thumbnail struct {
	Size string `json:"size"`
	URL  string `json:"url"`
}

func toMetadata(a avatar.Avatar) metadataResponse {
	thumbs := make([]thumbnail, 0, len(a.Thumbnails))
	for size := range a.Thumbnails {
		thumbs = append(thumbs, thumbnail{Size: size, URL: thumbnailURL(a.ID, size)})
	}
	// Map order is random; stable output is nicer for clients and tests.
	sort.Slice(thumbs, func(i, j int) bool { return thumbs[i].Size < thumbs[j].Size })

	return metadataResponse{
		ID:         a.ID,
		UserID:     a.UserID,
		FileName:   a.FileName,
		MimeType:   a.MimeType,
		Size:       a.SizeBytes,
		Dimensions: dimensions{Width: a.Width, Height: a.Height},
		Thumbnails: thumbs,
		URL:        originalURL(a.ID),
		Status:     a.ProcessingStatus,
		CreatedAt:  a.CreatedAt,
		UpdatedAt:  a.UpdatedAt,
	}
}

func (h *handlers) metadata(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.Metadata(r.Context(), chi.URLParam(r, "avatarID"))
	if err != nil {
		h.serviceError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMetadata(a))
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	avatars, err := h.svc.List(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		h.serviceError(r.Context(), w, err)
		return
	}
	items := make([]metadataResponse, 0, len(avatars))
	for _, a := range avatars {
		items = append(items, toMetadata(a))
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *handlers) deleteAvatar(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), chi.URLParam(r, "avatarID"), userID); err != nil {
		h.serviceError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) deleteLatest(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteLatest(r.Context(), chi.URLParam(r, "userID"), userID); err != nil {
		h.serviceError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serviceError maps domain errors to HTTP answers.
func (h *handlers) serviceError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, avatar.ErrNotFound):
		writeError(w, http.StatusNotFound, "Avatar not found")
	case errors.Is(err, avatar.ErrNotOwner):
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "Forbidden",
			"details": "You can only delete your own avatars",
		})
	default:
		h.log.ErrorContext(ctx, "service error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func originalURL(id string) string {
	return "/api/v1/avatars/" + id
}

func thumbnailURL(id, size string) string {
	return "/api/v1/avatars/" + id + "/thumbnails/" + size
}

func isTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

func writeTooLarge(w http.ResponseWriter) {
	writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
		"error":    "File too large",
		"max_size": maxUploadBytes,
	})
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	// The status line is out; an encode failure here can only be logged
	// by the caller, and in practice these bodies always marshal.
	_ = json.NewEncoder(w).Encode(body)
}
