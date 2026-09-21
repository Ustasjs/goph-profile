package avatar

// UploadEvent asks the worker to build thumbnails for a fresh upload.
type UploadEvent struct {
	AvatarID string `json:"avatar_id"`
	UserID   string `json:"user_id"`
	S3Key    string `json:"s3_key"`
}

// DeleteEvent asks the worker to remove the S3 objects of a
// soft-deleted avatar. The keys are captured at delete time, because
// the record is already hidden from reads.
type DeleteEvent struct {
	AvatarID string   `json:"avatar_id"`
	S3Keys   []string `json:"s3_keys"`
}
