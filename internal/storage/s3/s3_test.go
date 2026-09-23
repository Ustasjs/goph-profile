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

func TestNewRejectsIncompleteConfig(t *testing.T) {
	full := func() s3.Config {
		return s3.Config{Endpoint: "localhost:9000", AccessKey: "k", SecretKey: "s", Bucket: "b"}
	}

	tests := []struct {
		name   string
		mutate func(*s3.Config)
	}{
		{"no endpoint", func(c *s3.Config) { c.Endpoint = "" }},
		{"no access key", func(c *s3.Config) { c.AccessKey = "" }},
		{"no secret key", func(c *s3.Config) { c.SecretKey = "" }},
		{"no bucket", func(c *s3.Config) { c.Bucket = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := full()
			tt.mutate(&cfg)
			_, err := s3.New(cfg)
			assert.Error(t, err)
		})
	}

	_, err := s3.New(full())
	assert.NoError(t, err)
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
