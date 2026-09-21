package s3_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ustasjs/goph-profile/internal/avatar"
	"github.com/ustasjs/goph-profile/internal/storage/s3"
)

// newStore connects to the storage from S3_* variables. Without an
// endpoint the test is skipped, so plain "go test" works with no
// MinIO around.
func newStore(t *testing.T) *s3.Store {
	t.Helper()

	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3_ENDPOINT is not set")
	}

	store, err := s3.New(s3.Config{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("S3_ACCESS_KEY"),
		SecretKey: os.Getenv("S3_SECRET_KEY"),
		Bucket:    "test-avatars",
	})
	require.NoError(t, err)
	require.NoError(t, store.EnsureBucket(context.Background()))
	return store
}

func TestPutGetDelete(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	key := "test/" + uuid.NewString()
	payload := []byte("fake image bytes")

	require.NoError(t, store.Put(ctx, key, "image/png", bytes.NewReader(payload), int64(len(payload))))

	r, err := store.Get(ctx, key)
	require.NoError(t, err)
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	assert.Equal(t, payload, got)

	require.NoError(t, store.Delete(ctx, key))

	_, err = store.Get(ctx, key)
	assert.ErrorIs(t, err, avatar.ErrNotFound)
}

func TestDeleteMissingIsNoError(t *testing.T) {
	store := newStore(t)

	assert.NoError(t, store.Delete(context.Background(), "test/"+uuid.NewString()))
}

func TestPing(t *testing.T) {
	store := newStore(t)

	assert.NoError(t, store.Ping(context.Background()))
}
