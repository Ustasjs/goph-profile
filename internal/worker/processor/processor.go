// Package processor executes the async avatar jobs: thumbnail
// generation after an upload and S3 cleanup after a delete.
package processor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/ustasjs/goph-profile/internal/avatar"
	"github.com/ustasjs/goph-profile/internal/worker/thumbnail"
)

// tracer is bound lazily to the global provider. The consumer span
// wraps the whole delivery; these child spans separate the handler
// work (and each thumbnail) inside it.
var tracer = otel.Tracer("github.com/ustasjs/goph-profile/internal/worker/processor")

// Repository is the metadata storage the processor needs.
type Repository interface {
	GetByID(ctx context.Context, id string) (avatar.Avatar, error)
	SetProcessingStatus(ctx context.Context, id, status string) error
	SetThumbnails(ctx context.Context, id string, keys map[string]string) error
}

// FileStore is the object storage the processor needs.
type FileStore interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	Delete(ctx context.Context, key string) error
}

// Observer records processing outcomes; the metrics registry
// implements it. Each handler call is one attempt, so retries count
// separately.
type Observer interface {
	ObserveProcessed(event, status string, seconds float64)
}

// Outcome labels, matching the metrics package by value.
const (
	statusOK      = "ok"
	statusError   = "error"
	statusSkipped = "skipped"
)

// Processor handles consumed events.
type Processor struct {
	repo  Repository
	files FileStore
	obs   Observer
	log   *zap.Logger
}

// New builds the processor.
func New(repo Repository, files FileStore, obs Observer, log *zap.Logger) *Processor {
	return &Processor{repo: repo, files: files, obs: obs, log: log}
}

// HandleUpload builds and stores the thumbnails for one avatar.
// Deliveries can repeat, so the work is guarded by the current
// processing status.
func (p *Processor) HandleUpload(ctx context.Context, ev avatar.UploadEvent) (err error) {
	ctx, span := tracer.Start(ctx, "process_upload", trace.WithAttributes(
		attribute.String("avatar_id", ev.AvatarID),
		attribute.String("user_id", ev.UserID),
	))
	defer span.End()

	status := statusError
	start := time.Now()
	defer func() { p.obs.ObserveProcessed("upload", status, time.Since(start).Seconds()) }()

	a, err := p.repo.GetByID(ctx, ev.AvatarID)
	if errors.Is(err, avatar.ErrNotFound) {
		// Deleted (or never committed) while the event was in
		// flight: nothing to process.
		p.log.Info("upload event for a missing avatar, skipping",
			zap.String("avatar_id", ev.AvatarID))
		status = statusSkipped
		return nil
	}
	if err != nil {
		return err
	}
	if a.ProcessingStatus == avatar.ProcessingStatusCompleted ||
		a.ProcessingStatus == avatar.ProcessingStatusDeleted {
		// A repeated delivery: the work is already done.
		p.log.Info("avatar already processed, skipping",
			zap.String("avatar_id", ev.AvatarID),
			zap.String("status", a.ProcessingStatus))
		status = statusSkipped
		return nil
	}

	if err := p.repo.SetProcessingStatus(ctx, ev.AvatarID, avatar.ProcessingStatusProcessing); err != nil {
		return err
	}

	body, err := p.files.Get(ctx, ev.S3Key)
	if err != nil {
		return fmt.Errorf("download original: %w", err)
	}
	src, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		return fmt.Errorf("read original: %w", err)
	}

	keys := make(map[string]string, len(avatar.ThumbnailSizes))
	for _, px := range avatar.ThumbnailSizes {
		_, thumbSpan := tracer.Start(ctx, "generate_thumbnail",
			trace.WithAttributes(attribute.Int("size_px", px)))
		thumb, err := thumbnail.Generate(src, px)
		thumbSpan.End()
		if err != nil {
			// Not an image: retrying cannot help, so the avatar is
			// marked failed and the message is consumed.
			p.log.Warn("original does not decode, marking failed",
				zap.String("avatar_id", ev.AvatarID), zap.Error(err))
			return p.repo.SetProcessingStatus(ctx, ev.AvatarID, avatar.ProcessingStatusFailed)
		}

		key := avatar.ThumbnailKey(ev.AvatarID, px)
		if err := p.files.Put(ctx, key, "image/jpeg", bytes.NewReader(thumb), int64(len(thumb))); err != nil {
			return fmt.Errorf("store thumbnail %s: %w", key, err)
		}
		keys[avatar.SizeName(px)] = key
	}

	if err := p.repo.SetThumbnails(ctx, ev.AvatarID, keys); err != nil {
		return err
	}
	p.log.Info("thumbnails ready", zap.String("avatar_id", ev.AvatarID))
	status = statusOK
	return nil
}

// HandleDelete removes the S3 objects of a soft-deleted avatar. The
// store treats missing keys as success, so repeated deliveries are
// harmless.
func (p *Processor) HandleDelete(ctx context.Context, ev avatar.DeleteEvent) (err error) {
	ctx, span := tracer.Start(ctx, "process_delete", trace.WithAttributes(
		attribute.String("avatar_id", ev.AvatarID),
		attribute.Int("keys", len(ev.S3Keys)),
	))
	defer span.End()

	status := statusError
	start := time.Now()
	defer func() { p.obs.ObserveProcessed("delete", status, time.Since(start).Seconds()) }()

	for _, key := range ev.S3Keys {
		if err := p.files.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete object %s: %w", key, err)
		}
	}

	// The record is already soft-deleted and invisible: a missing
	// row here only means it never existed.
	err = p.repo.SetProcessingStatus(ctx, ev.AvatarID, avatar.ProcessingStatusDeleted)
	if err != nil && !errors.Is(err, avatar.ErrNotFound) {
		return err
	}
	p.log.Info("avatar files removed", zap.String("avatar_id", ev.AvatarID))
	status = statusOK
	return nil
}

// UploadFailed marks the avatar failed after the last retry, so a
// stuck "processing" is distinguishable from a broken one.
func (p *Processor) UploadFailed(ctx context.Context, ev avatar.UploadEvent) {
	if err := p.repo.SetProcessingStatus(ctx, ev.AvatarID, avatar.ProcessingStatusFailed); err != nil {
		p.log.Error("mark processing failed",
			zap.String("avatar_id", ev.AvatarID), zap.Error(err))
	}
}
