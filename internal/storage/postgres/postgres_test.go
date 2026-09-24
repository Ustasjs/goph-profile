package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ustasjs/goph-profile/internal/avatar"
	"github.com/ustasjs/goph-profile/internal/storage/postgres"
	"github.com/ustasjs/goph-profile/migrations"
)

// newRepo connects to the database from DATABASE_DSN and applies
// migrations. Without the variable the test is skipped, so plain
// "go test" works with no database around.
func newRepo(t *testing.T) *postgres.Repository {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN is not set")
	}
	require.NoError(t, migrations.Run(dsn))

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return postgres.New(pool)
}

func newAvatar(userID string) avatar.New {
	id := uuid.NewString()
	return avatar.New{
		ID:        id,
		UserID:    userID,
		FileName:  "avatar.png",
		MimeType:  "image/png",
		SizeBytes: 1234,
		Width:     640,
		Height:    480,
		S3Key:     avatar.OriginalKey(id),
	}
}

func TestCreateAndGet(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	n := newAvatar(uuid.NewString())
	created, err := repo.Create(ctx, n)
	require.NoError(t, err)

	assert.Equal(t, n.ID, created.ID)
	assert.Equal(t, avatar.UploadStatusUploading, created.UploadStatus)
	assert.Equal(t, avatar.ProcessingStatusPending, created.ProcessingStatus)
	assert.Empty(t, created.Thumbnails)
	assert.WithinDuration(t, time.Now(), created.CreatedAt, time.Minute)

	got, err := repo.GetByID(ctx, n.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, n.UserID, got.UserID)
	assert.Equal(t, 640, got.Width)
}

func TestGetMissing(t *testing.T) {
	repo := newRepo(t)

	_, err := repo.GetByID(context.Background(), uuid.NewString())
	assert.ErrorIs(t, err, avatar.ErrNotFound)
}

func TestLatestAndList(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	userID := uuid.NewString()

	first, err := repo.Create(ctx, newAvatar(userID))
	require.NoError(t, err)
	// created_at has microsecond precision; a tiny pause keeps the
	// ordering deterministic.
	time.Sleep(5 * time.Millisecond)
	second, err := repo.Create(ctx, newAvatar(userID))
	require.NoError(t, err)

	latest, err := repo.LatestByUser(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, second.ID, latest.ID)

	list, err := repo.ListByUser(ctx, userID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, second.ID, list[0].ID)
	assert.Equal(t, first.ID, list[1].ID)
}

func TestListEmpty(t *testing.T) {
	repo := newRepo(t)

	list, err := repo.ListByUser(context.Background(), uuid.NewString())
	require.NoError(t, err)
	assert.NotNil(t, list)
	assert.Empty(t, list)
}

func TestStatusUpdates(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	a, err := repo.Create(ctx, newAvatar(uuid.NewString()))
	require.NoError(t, err)

	require.NoError(t, repo.SetUploadStatus(ctx, a.ID, avatar.UploadStatusUploaded))
	require.NoError(t, repo.SetProcessingStatus(ctx, a.ID, avatar.ProcessingStatusProcessing))

	keys := map[string]string{
		"100x100": avatar.ThumbnailKey(a.ID, 100),
		"300x300": avatar.ThumbnailKey(a.ID, 300),
	}
	require.NoError(t, repo.SetThumbnails(ctx, a.ID, keys))

	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, avatar.UploadStatusUploaded, got.UploadStatus)
	assert.Equal(t, avatar.ProcessingStatusCompleted, got.ProcessingStatus)
	assert.Equal(t, keys, got.Thumbnails)
}

func TestStatusRejectsUnknownValue(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	a, err := repo.Create(ctx, newAvatar(uuid.NewString()))
	require.NoError(t, err)

	// The status columns are postgres enums: a value outside the
	// dictionary must be rejected by the database itself.
	assert.Error(t, repo.SetUploadStatus(ctx, a.ID, "exploded"))
	assert.Error(t, repo.SetProcessingStatus(ctx, a.ID, "exploded"))
}

func TestStatusUpdateMissing(t *testing.T) {
	repo := newRepo(t)

	err := repo.SetUploadStatus(context.Background(), uuid.NewString(), avatar.UploadStatusUploaded)
	assert.ErrorIs(t, err, avatar.ErrNotFound)
}

func TestSoftDelete(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	userID := uuid.NewString()

	a, err := repo.Create(ctx, newAvatar(userID))
	require.NoError(t, err)
	require.NoError(t, repo.SoftDelete(ctx, a.ID))

	_, err = repo.GetByID(ctx, a.ID)
	assert.ErrorIs(t, err, avatar.ErrNotFound)

	list, err := repo.ListByUser(ctx, userID)
	require.NoError(t, err)
	assert.Empty(t, list)

	// A second delete finds nothing to touch.
	assert.ErrorIs(t, repo.SoftDelete(ctx, a.ID), avatar.ErrNotFound)
}
