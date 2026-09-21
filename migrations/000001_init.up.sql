CREATE TABLE avatars (
    id                UUID PRIMARY KEY,
    user_id           TEXT        NOT NULL,
    file_name         TEXT        NOT NULL,
    mime_type         TEXT        NOT NULL,
    size_bytes        BIGINT      NOT NULL,
    width             INT         NOT NULL DEFAULT 0,
    height            INT         NOT NULL DEFAULT 0,
    s3_key            TEXT        NOT NULL,
    thumbnail_s3_keys JSONB       NOT NULL DEFAULT '{}',
    upload_status     TEXT        NOT NULL DEFAULT 'uploading',
    processing_status TEXT        NOT NULL DEFAULT 'pending',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ
);

CREATE INDEX idx_avatars_user_active ON avatars (user_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_avatars_statuses ON avatars (upload_status, processing_status);
