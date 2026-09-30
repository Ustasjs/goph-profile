// Package postgres stores avatar metadata in PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ustasjs/goph-profile/internal/avatar"
)

// Repository runs the avatar queries on a pgx pool.
type Repository struct {
	pool *pgxpool.Pool
}

// New wraps a ready pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// avatarColumns is the select list every read shares, in scanAvatar order.
const avatarColumns = `id, user_id, file_name, mime_type, size_bytes, width, height,
	s3_key, thumbnail_s3_keys, upload_status, processing_status,
	created_at, updated_at, deleted_at`

func scanAvatar(row pgx.Row) (avatar.Avatar, error) {
	var a avatar.Avatar
	err := row.Scan(&a.ID, &a.UserID, &a.FileName, &a.MimeType, &a.SizeBytes,
		&a.Width, &a.Height, &a.S3Key, &a.Thumbnails,
		&a.UploadStatus, &a.ProcessingStatus,
		&a.CreatedAt, &a.UpdatedAt, &a.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return avatar.Avatar{}, avatar.ErrNotFound
	}
	if err != nil {
		return avatar.Avatar{}, fmt.Errorf("scan avatar: %w", err)
	}
	return a, nil
}

// Create inserts a new record and returns it as stored.
func (r *Repository) Create(ctx context.Context, n avatar.New) (avatar.Avatar, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO avatars (id, user_id, file_name, mime_type, size_bytes, width, height, s3_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+avatarColumns,
		n.ID, n.UserID, n.FileName, n.MimeType, n.SizeBytes, n.Width, n.Height, n.S3Key)
	a, err := scanAvatar(row)
	if err != nil {
		return avatar.Avatar{}, fmt.Errorf("insert avatar: %w", err)
	}
	return a, nil
}

// GetByID returns one live (not deleted) avatar.
func (r *Repository) GetByID(ctx context.Context, id string) (avatar.Avatar, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+avatarColumns+` FROM avatars WHERE id = $1 AND deleted_at IS NULL`, id)
	return scanAvatar(row)
}

// LatestByUser returns the newest live avatar of the user.
func (r *Repository) LatestByUser(ctx context.Context, userID string) (avatar.Avatar, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+avatarColumns+` FROM avatars
		 WHERE user_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`, userID)
	return scanAvatar(row)
}

// ListByUser returns all live avatars of the user, newest first.
func (r *Repository) ListByUser(ctx context.Context, userID string) ([]avatar.Avatar, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+avatarColumns+` FROM avatars
		 WHERE user_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list avatars: %w", err)
	}
	defer rows.Close()

	// An empty slice, not nil: the handler renders it as [] in JSON.
	list := []avatar.Avatar{}
	for rows.Next() {
		a, err := scanAvatar(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list avatars: %w", err)
	}
	return list, nil
}

// SetUploadStatus moves the original-file status.
func (r *Repository) SetUploadStatus(ctx context.Context, id, status string) error {
	return r.exec(ctx,
		`UPDATE avatars SET upload_status = $2, updated_at = now() WHERE id = $1`, id, status)
}

// SetProcessingStatus moves the thumbnail-processing status.
func (r *Repository) SetProcessingStatus(ctx context.Context, id, status string) error {
	return r.exec(ctx,
		`UPDATE avatars SET processing_status = $2, updated_at = now() WHERE id = $1`, id, status)
}

// SetThumbnails stores the thumbnail keys and marks processing done.
func (r *Repository) SetThumbnails(ctx context.Context, id string, keys map[string]string) error {
	return r.exec(ctx,
		`UPDATE avatars SET thumbnail_s3_keys = $2, processing_status = $3, updated_at = now()
		 WHERE id = $1`, id, keys, avatar.ProcessingStatusCompleted)
}

// SoftDelete hides the avatar from all reads. The S3 objects are
// removed later, asynchronously.
func (r *Repository) SoftDelete(ctx context.Context, id string) error {
	return r.exec(ctx,
		`UPDATE avatars SET deleted_at = now(), updated_at = now()
		 WHERE id = $1 AND deleted_at IS NULL`, id)
}

// StorageByUser sums the live avatar bytes per user. The metrics
// collector calls it on every scrape, so it must stay one cheap
// grouped query.
func (r *Repository) StorageByUser(ctx context.Context) (map[string]int64, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT user_id, SUM(size_bytes) FROM avatars
		 WHERE deleted_at IS NULL
		 GROUP BY user_id`)
	if err != nil {
		return nil, fmt.Errorf("sum storage: %w", err)
	}
	defer rows.Close()

	usage := map[string]int64{}
	for rows.Next() {
		var userID string
		var bytes int64
		if err := rows.Scan(&userID, &bytes); err != nil {
			return nil, fmt.Errorf("scan storage row: %w", err)
		}
		usage[userID] = bytes
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sum storage: %w", err)
	}
	return usage, nil
}

// exec runs one UPDATE and turns "no rows touched" into ErrNotFound.
func (r *Repository) exec(ctx context.Context, sql string, args ...any) error {
	tag, err := r.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("update avatar: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return avatar.ErrNotFound
	}
	return nil
}
