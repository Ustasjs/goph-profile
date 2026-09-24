package processor

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/ustasjs/goph-profile/internal/avatar"
)

// fakeRepo keeps one avatar and records status transitions.
type fakeRepo struct {
	avatars    map[string]avatar.Avatar
	statuses   []string
	thumbnails map[string]string
}

func newFakeRepo(avatars ...avatar.Avatar) *fakeRepo {
	r := &fakeRepo{avatars: map[string]avatar.Avatar{}}
	for _, a := range avatars {
		r.avatars[a.ID] = a
	}
	return r
}

func (r *fakeRepo) GetByID(_ context.Context, id string) (avatar.Avatar, error) {
	a, ok := r.avatars[id]
	if !ok || a.DeletedAt != nil {
		return avatar.Avatar{}, avatar.ErrNotFound
	}
	return a, nil
}

func (r *fakeRepo) SetProcessingStatus(_ context.Context, id, status string) error {
	if _, ok := r.avatars[id]; !ok {
		return avatar.ErrNotFound
	}
	a := r.avatars[id]
	a.ProcessingStatus = status
	r.avatars[id] = a
	r.statuses = append(r.statuses, status)
	return nil
}

func (r *fakeRepo) SetThumbnails(_ context.Context, id string, keys map[string]string) error {
	if _, ok := r.avatars[id]; !ok {
		return avatar.ErrNotFound
	}
	a := r.avatars[id]
	a.Thumbnails = keys
	a.ProcessingStatus = avatar.ProcessingStatusCompleted
	r.avatars[id] = a
	r.thumbnails = keys
	r.statuses = append(r.statuses, avatar.ProcessingStatusCompleted)
	return nil
}

// fakeFiles keeps objects in a map.
type fakeFiles struct {
	objects   map[string][]byte
	deleted   []string
	deleteErr error
}

func newFakeFiles() *fakeFiles {
	return &fakeFiles{objects: map[string][]byte{}}
}

func (f *fakeFiles) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := f.objects[key]
	if !ok {
		return nil, avatar.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeFiles) Put(_ context.Context, key, _ string, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.objects[key] = data
	return nil
}

func (f *fakeFiles) Delete(_ context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	// Missing keys are fine: the real store is idempotent too.
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))))
	return buf.Bytes()
}

func pendingAvatar(id string) avatar.Avatar {
	return avatar.Avatar{
		ID:               id,
		UserID:           "u1",
		S3Key:            avatar.OriginalKey(id),
		ProcessingStatus: avatar.ProcessingStatusPending,
	}
}

func TestHandleUpload(t *testing.T) {
	a := pendingAvatar("a1")
	repo := newFakeRepo(a)
	files := newFakeFiles()
	files.objects[a.S3Key] = pngBytes(t, 640, 480)
	p := New(repo, files, zap.NewNop())

	ev := avatar.UploadEvent{AvatarID: a.ID, UserID: a.UserID, S3Key: a.S3Key}
	require.NoError(t, p.HandleUpload(context.Background(), ev))

	// Both thumbnails stored under their keys, statuses walked
	// processing -> completed.
	assert.Equal(t, []string{avatar.ProcessingStatusProcessing, avatar.ProcessingStatusCompleted}, repo.statuses)
	require.Len(t, repo.thumbnails, 2)
	for _, px := range avatar.ThumbnailSizes {
		key := avatar.ThumbnailKey(a.ID, px)
		assert.Equal(t, key, repo.thumbnails[avatar.SizeName(px)])
		assert.Contains(t, files.objects, key)
	}
}

func TestHandleUploadIdempotent(t *testing.T) {
	a := pendingAvatar("a1")
	a.ProcessingStatus = avatar.ProcessingStatusCompleted
	repo := newFakeRepo(a)
	files := newFakeFiles() // empty: any S3 access would fail the test
	p := New(repo, files, zap.NewNop())

	ev := avatar.UploadEvent{AvatarID: a.ID, S3Key: a.S3Key}
	require.NoError(t, p.HandleUpload(context.Background(), ev))
	assert.Empty(t, repo.statuses)
}

func TestHandleUploadMissingAvatar(t *testing.T) {
	p := New(newFakeRepo(), newFakeFiles(), zap.NewNop())

	ev := avatar.UploadEvent{AvatarID: "gone", S3Key: "k"}
	assert.NoError(t, p.HandleUpload(context.Background(), ev))
}

func TestHandleUploadMissingObjectIsRetryable(t *testing.T) {
	a := pendingAvatar("a1")
	repo := newFakeRepo(a)
	p := New(repo, newFakeFiles(), zap.NewNop())

	ev := avatar.UploadEvent{AvatarID: a.ID, S3Key: a.S3Key}
	// The object may simply not be visible yet: the error must
	// propagate so the consumer retries.
	assert.Error(t, p.HandleUpload(context.Background(), ev))
}

func TestHandleUploadNonImageMarksFailed(t *testing.T) {
	a := pendingAvatar("a1")
	repo := newFakeRepo(a)
	files := newFakeFiles()
	files.objects[a.S3Key] = []byte("not an image at all")
	p := New(repo, files, zap.NewNop())

	ev := avatar.UploadEvent{AvatarID: a.ID, S3Key: a.S3Key}
	// No error: retrying a broken file cannot help.
	require.NoError(t, p.HandleUpload(context.Background(), ev))
	assert.Equal(t, []string{avatar.ProcessingStatusProcessing, avatar.ProcessingStatusFailed}, repo.statuses)
}

func TestHandleDelete(t *testing.T) {
	a := pendingAvatar("a1")
	repo := newFakeRepo(a)
	files := newFakeFiles()
	files.objects[a.S3Key] = []byte("data")
	thumbKey := avatar.ThumbnailKey(a.ID, 100)
	files.objects[thumbKey] = []byte("thumb")
	p := New(repo, files, zap.NewNop())

	ev := avatar.DeleteEvent{AvatarID: a.ID, S3Keys: []string{a.S3Key, thumbKey}}
	require.NoError(t, p.HandleDelete(context.Background(), ev))

	assert.Empty(t, files.objects)
	assert.Equal(t, []string{avatar.ProcessingStatusDeleted}, repo.statuses)
}

func TestHandleDeleteMissingRowTolerated(t *testing.T) {
	p := New(newFakeRepo(), newFakeFiles(), zap.NewNop())

	ev := avatar.DeleteEvent{AvatarID: "gone", S3Keys: []string{"k1"}}
	assert.NoError(t, p.HandleDelete(context.Background(), ev))
}

func TestHandleDeleteS3FailureIsRetryable(t *testing.T) {
	files := newFakeFiles()
	files.deleteErr = errors.New("s3 is down")
	p := New(newFakeRepo(), files, zap.NewNop())

	ev := avatar.DeleteEvent{AvatarID: "a1", S3Keys: []string{"k1"}}
	assert.Error(t, p.HandleDelete(context.Background(), ev))
}

func TestUploadFailed(t *testing.T) {
	a := pendingAvatar("a1")
	repo := newFakeRepo(a)
	p := New(repo, newFakeFiles(), zap.NewNop())

	p.UploadFailed(context.Background(), avatar.UploadEvent{AvatarID: a.ID})
	assert.Equal(t, []string{avatar.ProcessingStatusFailed}, repo.statuses)
}
