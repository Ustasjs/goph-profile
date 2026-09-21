// Package avatar holds the domain model shared by the server and
// the worker: the avatar record, its statuses and the S3 key layout.
package avatar

import (
	"errors"
	"fmt"
	"time"
)

// Upload statuses: the life of the original file in S3.
const (
	UploadStatusUploading = "uploading"
	UploadStatusUploaded  = "uploaded"
	UploadStatusFailed    = "failed"
)

// Processing statuses: the life of the thumbnails made by the worker.
const (
	ProcessingStatusPending    = "pending"
	ProcessingStatusProcessing = "processing"
	ProcessingStatusCompleted  = "completed"
	ProcessingStatusFailed     = "failed"
	ProcessingStatusDeleted    = "deleted"
)

// ThumbnailSizes lists the square thumbnail sizes the worker makes,
// in pixels.
var ThumbnailSizes = []int{100, 300}

var (
	// ErrNotFound means the avatar does not exist or is deleted.
	ErrNotFound = errors.New("avatar not found")
	// ErrNotOwner means the caller tried to touch someone else's avatar.
	ErrNotOwner = errors.New("avatar belongs to another user")
)

// Avatar is one stored avatar record.
type Avatar struct {
	ID       string
	UserID   string
	FileName string
	MimeType string
	// SizeBytes is the original file size.
	SizeBytes int64
	// Width and Height are the original dimensions. Zero when the
	// file could not be decoded as an image.
	Width  int
	Height int
	// S3Key locates the original file.
	S3Key string
	// Thumbnails maps a size name ("100x100") to its S3 key. Empty
	// until the worker finishes.
	Thumbnails       map[string]string
	UploadStatus     string
	ProcessingStatus string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

// New is the data needed to insert an avatar record. The ID and
// S3Key are chosen by the caller before the insert, so the S3
// object can be written under its final key.
type New struct {
	ID        string
	UserID    string
	FileName  string
	MimeType  string
	SizeBytes int64
	Width     int
	Height    int
	S3Key     string
}

// OriginalKey is the S3 key of the original file.
func OriginalKey(id string) string {
	return "avatars/" + id + "/original"
}

// ThumbnailKey is the S3 key of one thumbnail. Thumbnails are
// always JPEG regardless of the original format.
func ThumbnailKey(id string, px int) string {
	return fmt.Sprintf("thumbnails/%s/%s.jpg", id, SizeName(px))
}

// SizeName renders a pixel size as the public size label ("100x100").
func SizeName(px int) string {
	return fmt.Sprintf("%dx%d", px, px)
}
