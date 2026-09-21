// Package service implements the avatar use cases on top of the
// metadata repository and the file store.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"

	// Register decoders for dimension probing.
	_ "image/jpeg"
	_ "image/png"

	"github.com/google/uuid"
	"go.uber.org/zap"

	// Register the WebP decoder too: WebP has no stdlib decoder,
	// and x/image supports decode only, which is all the probe needs.
	_ "golang.org/x/image/webp"

	"github.com/ustasjs/goph-profile/internal/avatar"
)

// Repository is the metadata storage the service needs.
type Repository interface {
	Create(ctx context.Context, n avatar.New) (avatar.Avatar, error)
	GetByID(ctx context.Context, id string) (avatar.Avatar, error)
	LatestByUser(ctx context.Context, userID string) (avatar.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]avatar.Avatar, error)
	SetUploadStatus(ctx context.Context, id, status string) error
	SoftDelete(ctx context.Context, id string) error
}

// FileStore is the object storage the service needs.
type FileStore interface {
	Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Publisher emits the async jobs for the worker.
type Publisher interface {
	PublishUpload(ctx context.Context, ev avatar.UploadEvent) error
	PublishDelete(ctx context.Context, ev avatar.DeleteEvent) error
}

// Service wires the avatar use cases together.
type Service struct {
	repo  Repository
	files FileStore
	pub   Publisher
	log   *zap.Logger
}

// New builds the service.
func New(repo Repository, files FileStore, pub Publisher, log *zap.Logger) *Service {
	return &Service{repo: repo, files: files, pub: pub, log: log}
}

// File is an opened avatar file ready for streaming.
type File struct {
	Body        io.ReadCloser
	ContentType string
	Size        int64
}

// Upload stores the file and its metadata. The record is created
// first, so a failed S3 write leaves a visible failed row instead
// of an orphaned object.
func (s *Service) Upload(ctx context.Context, userID, fileName string, data []byte) (avatar.Avatar, error) {
	id := uuid.NewString()

	// Dimensions are best effort: format validation is out of
	// scope, so a file that is not an image is stored with zero
	// dimensions rather than rejected.
	var width, height int
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		width, height = cfg.Width, cfg.Height
	}

	a, err := s.repo.Create(ctx, avatar.New{
		ID:        id,
		UserID:    userID,
		FileName:  fileName,
		MimeType:  http.DetectContentType(data),
		SizeBytes: int64(len(data)),
		Width:     width,
		Height:    height,
		S3Key:     avatar.OriginalKey(id),
	})
	if err != nil {
		return avatar.Avatar{}, fmt.Errorf("create avatar record: %w", err)
	}

	if err := s.files.Put(ctx, a.S3Key, a.MimeType, bytes.NewReader(data), a.SizeBytes); err != nil {
		// Best-effort compensation: mark the row failed so the
		// stuck upload is visible; the original error matters more.
		if markErr := s.repo.SetUploadStatus(ctx, a.ID, avatar.UploadStatusFailed); markErr != nil {
			s.log.Error("mark upload failed", zap.String("avatar_id", a.ID), zap.Error(markErr))
		}
		return avatar.Avatar{}, fmt.Errorf("store avatar file: %w", err)
	}

	if err := s.repo.SetUploadStatus(ctx, a.ID, avatar.UploadStatusUploaded); err != nil {
		return avatar.Avatar{}, fmt.Errorf("finish avatar upload: %w", err)
	}
	a.UploadStatus = avatar.UploadStatusUploaded

	// A publish failure is not an upload failure: the file and the
	// record are safe, only the thumbnails will be missing. Answer
	// 201 and leave a trace for the operator.
	err = s.pub.PublishUpload(ctx, avatar.UploadEvent{
		AvatarID: a.ID,
		UserID:   a.UserID,
		S3Key:    a.S3Key,
	})
	if err != nil {
		s.log.Error("publish upload event", zap.String("avatar_id", a.ID), zap.Error(err))
	}
	return a, nil
}

// Metadata returns one avatar record.
func (s *Service) Metadata(ctx context.Context, id string) (avatar.Avatar, error) {
	return s.repo.GetByID(ctx, id)
}

// List returns all live avatars of the user, newest first.
func (s *Service) List(ctx context.Context, userID string) ([]avatar.Avatar, error) {
	return s.repo.ListByUser(ctx, userID)
}

// GetFile opens the original file of one avatar.
func (s *Service) GetFile(ctx context.Context, id string) (File, error) {
	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return File{}, err
	}
	return s.openOriginal(ctx, a)
}

// LatestFile opens the original of the newest avatar of the user.
func (s *Service) LatestFile(ctx context.Context, userID string) (File, error) {
	a, err := s.repo.LatestByUser(ctx, userID)
	if err != nil {
		return File{}, err
	}
	return s.openOriginal(ctx, a)
}

// ThumbnailFile opens one thumbnail by its size name ("100x100").
// Until the worker finishes there is no thumbnail, which reads as
// not found.
func (s *Service) ThumbnailFile(ctx context.Context, id, size string) (File, error) {
	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return File{}, err
	}
	key, ok := a.Thumbnails[size]
	if !ok {
		return File{}, avatar.ErrNotFound
	}
	body, err := s.files.Get(ctx, key)
	if err != nil {
		return File{}, err
	}
	return File{Body: body, ContentType: "image/jpeg"}, nil
}

// Delete soft-deletes one avatar after the ownership check. The S3
// objects are removed asynchronously by the worker.
func (s *Service) Delete(ctx context.Context, id, requesterID string) error {
	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if a.UserID != requesterID {
		return avatar.ErrNotOwner
	}
	return s.deleteAvatar(ctx, a)
}

// DeleteLatest soft-deletes the newest avatar of the user. Only the
// user themselves may do it.
func (s *Service) DeleteLatest(ctx context.Context, userID, requesterID string) error {
	if userID != requesterID {
		return avatar.ErrNotOwner
	}
	a, err := s.repo.LatestByUser(ctx, userID)
	if err != nil {
		return err
	}
	return s.deleteAvatar(ctx, a)
}

// deleteAvatar hides the record and schedules the S3 cleanup. The
// keys are captured now: after the soft delete the record is
// invisible to reads.
func (s *Service) deleteAvatar(ctx context.Context, a avatar.Avatar) error {
	keys := make([]string, 0, 1+len(a.Thumbnails))
	keys = append(keys, a.S3Key)
	for _, key := range a.Thumbnails {
		keys = append(keys, key)
	}

	if err := s.repo.SoftDelete(ctx, a.ID); err != nil {
		return err
	}

	// Same policy as uploads: the API answer does not depend on the
	// broker. Unpublished cleanup leaves orphaned objects, which is
	// acceptable for the MVP.
	err := s.pub.PublishDelete(ctx, avatar.DeleteEvent{AvatarID: a.ID, S3Keys: keys})
	if err != nil {
		s.log.Error("publish delete event", zap.String("avatar_id", a.ID), zap.Error(err))
	}
	return nil
}

func (s *Service) openOriginal(ctx context.Context, a avatar.Avatar) (File, error) {
	body, err := s.files.Get(ctx, a.S3Key)
	if err != nil {
		if errors.Is(err, avatar.ErrNotFound) {
			// The row exists but the object is gone (failed upload,
			// manual cleanup). To the client that is a missing avatar.
			return File{}, avatar.ErrNotFound
		}
		return File{}, err
	}
	return File{Body: body, ContentType: a.MimeType, Size: a.SizeBytes}, nil
}
