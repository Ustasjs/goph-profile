// Package s3 stores avatar files in any S3-compatible storage
// (MinIO locally) behind a small interface the rest of the app uses.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ustasjs/goph-profile/internal/avatar"
)

// tracer is bound lazily to the global provider. minio-go has no
// official instrumentation, so the wrapper methods trace themselves.
var tracer = otel.Tracer("github.com/ustasjs/goph-profile/internal/storage/s3")

// startSpan opens a client span for one storage operation.
func (s *Store) startSpan(ctx context.Context, op, key string) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{attribute.String("s3.bucket", s.bucket)}
	if key != "" {
		attrs = append(attrs, attribute.String("s3.key", key))
	}
	return tracer.Start(ctx, op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
}

// finishSpan closes the span, marking it failed when err is a real
// error. A missing object (avatar.ErrNotFound) is an expected answer,
// not a storage failure.
func finishSpan(span trace.Span, err error) {
	if err != nil && !errors.Is(err, avatar.ErrNotFound) {
		span.RecordError(err)
		span.SetStatus(codes.Error, "s3 operation failed")
	}
	span.End()
}

// Config carries the connection settings.
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// Store is an S3 client bound to one bucket.
type Store struct {
	client *minio.Client
	bucket string
}

// New builds the client. It does not touch the network: call
// EnsureBucket or Ping to check the connection.
func New(cfg Config) (*Store, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("s3: endpoint is required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("s3: access key and secret key are required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("s3: bucket is required")
	}

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create s3 client: %w", err)
	}
	return &Store{client: client, bucket: cfg.Bucket}, nil
}

// EnsureBucket creates the bucket when it does not exist yet.
func (s *Store) EnsureBucket(ctx context.Context) (err error) {
	ctx, span := s.startSpan(ctx, "s3.EnsureBucket", "")
	defer func() { finishSpan(span, err) }()

	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket: %w", err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		// Another process (the compose mc job) may create the
		// bucket between the check and the make.
		exists, checkErr := s.client.BucketExists(ctx, s.bucket)
		if checkErr == nil && exists {
			return nil
		}
		return fmt.Errorf("create bucket: %w", err)
	}
	return nil
}

// Ping reports whether the storage answers. Used by /health.
func (s *Store) Ping(ctx context.Context) error {
	if _, err := s.client.BucketExists(ctx, s.bucket); err != nil {
		return fmt.Errorf("ping s3: %w", err)
	}
	return nil
}

// Put writes one object.
func (s *Store) Put(ctx context.Context, key, contentType string, r io.Reader, size int64) (err error) {
	ctx, span := s.startSpan(ctx, "s3.Put", key)
	defer func() { finishSpan(span, err) }()

	_, err = s.client.PutObject(ctx, s.bucket, key, r, size,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// Get opens one object for reading. A missing object maps to
// avatar.ErrNotFound so callers can answer 404 without knowing S3.
// The span covers the open and the existence check, not the streaming
// that the caller does afterwards.
func (s *Store) Get(ctx context.Context, key string) (_ io.ReadCloser, err error) {
	ctx, span := s.startSpan(ctx, "s3.Get", key)
	defer func() { finishSpan(span, err) }()

	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	// GetObject is lazy: the first read reports a missing key. Stat
	// forces the check here, so the caller gets a clean 404 instead
	// of a broken stream.
	if _, statErr := obj.Stat(); statErr != nil {
		_ = obj.Close()
		if isNoSuchKey(statErr) {
			err = avatar.ErrNotFound
			return nil, err
		}
		err = fmt.Errorf("stat %s: %w", key, statErr)
		return nil, err
	}
	return obj, nil
}

// Delete removes one object. Deleting a missing object is not an
// error: the operation is idempotent by design.
func (s *Store) Delete(ctx context.Context, key string) (err error) {
	ctx, span := s.startSpan(ctx, "s3.Delete", key)
	defer func() { finishSpan(span, err) }()

	if err = s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		if isNoSuchKey(err) {
			err = nil
			return nil
		}
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

func isNoSuchKey(err error) bool {
	var resp minio.ErrorResponse
	return errors.As(err, &resp) && resp.Code == "NoSuchKey"
}
