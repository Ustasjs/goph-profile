package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ustasjs/goph-profile/internal/avatar"
)

// fakeRepo keeps avatars in a map and records status transitions.
type fakeRepo struct {
	avatars  map[string]avatar.Avatar
	statuses []string

	createErr error
	statusErr error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{avatars: map[string]avatar.Avatar{}}
}

func (r *fakeRepo) Create(_ context.Context, n avatar.New) (avatar.Avatar, error) {
	if r.createErr != nil {
		return avatar.Avatar{}, r.createErr
	}
	a := avatar.Avatar{
		ID: n.ID, UserID: n.UserID, FileName: n.FileName, MimeType: n.MimeType,
		SizeBytes: n.SizeBytes, Width: n.Width, Height: n.Height, S3Key: n.S3Key,
		UploadStatus:     avatar.UploadStatusUploading,
		ProcessingStatus: avatar.ProcessingStatusPending,
	}
	r.avatars[a.ID] = a
	return a, nil
}

func (r *fakeRepo) GetByID(_ context.Context, id string) (avatar.Avatar, error) {
	a, ok := r.avatars[id]
	if !ok || a.DeletedAt != nil {
		return avatar.Avatar{}, avatar.ErrNotFound
	}
	return a, nil
}

func (r *fakeRepo) LatestByUser(_ context.Context, userID string) (avatar.Avatar, error) {
	// The fake keeps at most one avatar per user in these tests.
	for _, a := range r.avatars {
		if a.UserID == userID && a.DeletedAt == nil {
			return a, nil
		}
	}
	return avatar.Avatar{}, avatar.ErrNotFound
}

func (r *fakeRepo) ListByUser(_ context.Context, userID string) ([]avatar.Avatar, error) {
	list := []avatar.Avatar{}
	for _, a := range r.avatars {
		if a.UserID == userID && a.DeletedAt == nil {
			list = append(list, a)
		}
	}
	return list, nil
}

func (r *fakeRepo) SetUploadStatus(_ context.Context, id, status string) error {
	if r.statusErr != nil {
		return r.statusErr
	}
	a := r.avatars[id]
	a.UploadStatus = status
	r.avatars[id] = a
	r.statuses = append(r.statuses, status)
	return nil
}

func (r *fakeRepo) SoftDelete(_ context.Context, id string) error {
	a, ok := r.avatars[id]
	if !ok {
		return avatar.ErrNotFound
	}
	now := a.CreatedAt
	a.DeletedAt = &now
	r.avatars[id] = a
	return nil
}

// fakeFiles keeps objects in a map.
type fakeFiles struct {
	objects map[string][]byte
	putErr  error
}

func newFakeFiles() *fakeFiles {
	return &fakeFiles{objects: map[string][]byte{}}
}

func (f *fakeFiles) Put(_ context.Context, key, _ string, r io.Reader, _ int64) error {
	if f.putErr != nil {
		return f.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.objects[key] = data
	return nil
}

func (f *fakeFiles) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := f.objects[key]
	if !ok {
		return nil, avatar.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// fakePub records published events.
type fakePub struct {
	uploads []avatar.UploadEvent
	deletes []avatar.DeleteEvent
	err     error
}

func (p *fakePub) PublishUpload(_ context.Context, ev avatar.UploadEvent) error {
	if p.err != nil {
		return p.err
	}
	p.uploads = append(p.uploads, ev)
	return nil
}

func (p *fakePub) PublishDelete(_ context.Context, ev avatar.DeleteEvent) error {
	if p.err != nil {
		return p.err
	}
	p.deletes = append(p.deletes, ev)
	return nil
}

func newService(repo *fakeRepo, files *fakeFiles) *Service {
	return New(repo, files, &fakePub{}, slog.New(slog.DiscardHandler))
}

// pngBytes renders a real PNG so DecodeConfig has something to read.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))))
	return buf.Bytes()
}

func TestUpload(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	pub := &fakePub{}
	svc := New(repo, files, pub, slog.New(slog.DiscardHandler))

	a, err := svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 640, 480))
	require.NoError(t, err)

	assert.Equal(t, "u1", a.UserID)
	assert.Equal(t, "image/png", a.MimeType)
	assert.Equal(t, 640, a.Width)
	assert.Equal(t, 480, a.Height)
	assert.Equal(t, avatar.UploadStatusUploaded, a.UploadStatus)
	assert.Contains(t, files.objects, a.S3Key)
	assert.Equal(t, []string{avatar.UploadStatusUploaded}, repo.statuses)

	// The thumbnail job is on its way to the worker.
	require.Len(t, pub.uploads, 1)
	assert.Equal(t, avatar.UploadEvent{AvatarID: a.ID, UserID: "u1", S3Key: a.S3Key}, pub.uploads[0])
}

func TestUploadPublishFailureStillSucceeds(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	pub := &fakePub{err: errors.New("broker is down")}
	svc := New(repo, files, pub, slog.New(slog.DiscardHandler))

	a, err := svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 1, 1))
	require.NoError(t, err)
	assert.Equal(t, avatar.UploadStatusUploaded, a.UploadStatus)
}

func TestUploadTruncatesLongFileName(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	svc := newService(repo, files)

	longName := strings.Repeat("ф", maxFileNameLen+40) + ".png"
	a, err := svc.Upload(context.Background(), "u1", longName, pngBytes(t, 1, 1))
	require.NoError(t, err)

	// Truncated to the column limit in characters, not bytes.
	assert.Equal(t, maxFileNameLen, len([]rune(a.FileName)))
	assert.Equal(t, strings.Repeat("ф", maxFileNameLen), a.FileName)
}

func TestUploadNonImage(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeFiles())

	// Not an image: stored anyway, dimensions stay zero.
	a, err := svc.Upload(context.Background(), "u1", "notes.txt", []byte("just text"))
	require.NoError(t, err)
	assert.Zero(t, a.Width)
	assert.Zero(t, a.Height)
}

func TestUploadS3FailureMarksRowFailed(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	files.putErr = errors.New("s3 is down")
	svc := newService(repo, files)

	_, err := svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 1, 1))
	require.Error(t, err)
	assert.Equal(t, []string{avatar.UploadStatusFailed}, repo.statuses)
}

func TestGetFile(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	svc := newService(repo, files)
	data := pngBytes(t, 2, 2)

	a, err := svc.Upload(context.Background(), "u1", "pic.png", data)
	require.NoError(t, err)

	f, err := svc.GetFile(context.Background(), a.ID)
	require.NoError(t, err)
	defer func() { _ = f.Body.Close() }()

	got, err := io.ReadAll(f.Body)
	require.NoError(t, err)
	assert.Equal(t, data, got)
	assert.Equal(t, "image/png", f.ContentType)
	assert.Equal(t, int64(len(data)), f.Size)
}

func TestGetFileMissing(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeFiles())

	_, err := svc.GetFile(context.Background(), "no-such-id")
	assert.ErrorIs(t, err, avatar.ErrNotFound)
}

func TestThumbnailMissingUntilProcessed(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	svc := newService(repo, files)

	a, err := svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 4, 4))
	require.NoError(t, err)

	// The worker has not run: thumbnails read as not found.
	_, err = svc.ThumbnailFile(context.Background(), a.ID, "100x100")
	assert.ErrorIs(t, err, avatar.ErrNotFound)

	// Once the keys appear, the thumbnail streams.
	key := avatar.ThumbnailKey(a.ID, 100)
	files.objects[key] = []byte("jpeg bytes")
	stored := repo.avatars[a.ID]
	stored.Thumbnails = map[string]string{"100x100": key}
	repo.avatars[a.ID] = stored

	f, err := svc.ThumbnailFile(context.Background(), a.ID, "100x100")
	require.NoError(t, err)
	defer func() { _ = f.Body.Close() }()
	assert.Equal(t, "image/jpeg", f.ContentType)
}

func TestDeleteOwnership(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	pub := &fakePub{}
	svc := New(repo, files, pub, slog.New(slog.DiscardHandler))

	a, err := svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 1, 1))
	require.NoError(t, err)

	err = svc.Delete(context.Background(), a.ID, "intruder")
	assert.ErrorIs(t, err, avatar.ErrNotOwner)
	assert.Empty(t, pub.deletes)

	require.NoError(t, svc.Delete(context.Background(), a.ID, "u1"))
	_, err = svc.GetFile(context.Background(), a.ID)
	assert.ErrorIs(t, err, avatar.ErrNotFound)

	// The cleanup event carries the original key.
	require.Len(t, pub.deletes, 1)
	assert.Equal(t, a.ID, pub.deletes[0].AvatarID)
	assert.Equal(t, []string{a.S3Key}, pub.deletes[0].S3Keys)
}

func TestDeleteCollectsThumbnailKeys(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	pub := &fakePub{}
	svc := New(repo, files, pub, slog.New(slog.DiscardHandler))

	a, err := svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 1, 1))
	require.NoError(t, err)

	// The worker has finished: the record carries thumbnail keys.
	stored := repo.avatars[a.ID]
	stored.Thumbnails = map[string]string{
		"100x100": avatar.ThumbnailKey(a.ID, 100),
		"300x300": avatar.ThumbnailKey(a.ID, 300),
	}
	repo.avatars[a.ID] = stored

	require.NoError(t, svc.Delete(context.Background(), a.ID, "u1"))
	require.Len(t, pub.deletes, 1)
	assert.ElementsMatch(t, []string{
		a.S3Key,
		avatar.ThumbnailKey(a.ID, 100),
		avatar.ThumbnailKey(a.ID, 300),
	}, pub.deletes[0].S3Keys)
}

func TestUploadCreateFailure(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	repo.createErr = errors.New("db is down")
	svc := newService(repo, files)

	_, err := svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 1, 1))
	assert.Error(t, err)
}

func TestMetadataAndList(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	svc := newService(repo, files)

	a, err := svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 1, 1))
	require.NoError(t, err)

	got, err := svc.Metadata(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, a.ID, got.ID)

	list, err := svc.List(context.Background(), "u1")
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestLatestFile(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	svc := newService(repo, files)

	_, err := svc.LatestFile(context.Background(), "u1")
	assert.ErrorIs(t, err, avatar.ErrNotFound)

	_, err = svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 1, 1))
	require.NoError(t, err)

	f, err := svc.LatestFile(context.Background(), "u1")
	require.NoError(t, err)
	require.NoError(t, f.Body.Close())
	assert.Equal(t, "image/png", f.ContentType)
}

func TestGetFileObjectGone(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	svc := newService(repo, files)

	a, err := svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 1, 1))
	require.NoError(t, err)

	// The record survived but the object vanished: to the client the
	// avatar is simply missing.
	delete(files.objects, a.S3Key)
	_, err = svc.GetFile(context.Background(), a.ID)
	assert.ErrorIs(t, err, avatar.ErrNotFound)
}

func TestDeleteLatest(t *testing.T) {
	repo, files := newFakeRepo(), newFakeFiles()
	svc := newService(repo, files)

	_, err := svc.Upload(context.Background(), "u1", "pic.png", pngBytes(t, 1, 1))
	require.NoError(t, err)

	assert.ErrorIs(t, svc.DeleteLatest(context.Background(), "u1", "u2"), avatar.ErrNotOwner)
	require.NoError(t, svc.DeleteLatest(context.Background(), "u1", "u1"))
	assert.ErrorIs(t, svc.DeleteLatest(context.Background(), "u1", "u1"), avatar.ErrNotFound)
}
