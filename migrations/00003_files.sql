-- +goose Up

-- Files: metadata in Postgres, bytes in object storage.
--
-- The predecessor kept this in a DynamoDB-shaped key/value store, which was the
-- only durable thing in that datastore and the reason it existed at all. It is a
-- table. Modelling the columns that get filtered on, rather than dumping
-- everything into a jsonb blob, is what makes "find the attachments on this
-- document" an index scan instead of a full scan.
CREATE TABLE files (
    id            uuid        PRIMARY KEY,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE RESTRICT,

    -- storage_key is the object key. It is generated, never derived from the
    -- user's filename: a filename is attacker-controlled and would otherwise
    -- let someone write outside their prefix or collide with another org.
    storage_key   text        NOT NULL UNIQUE,
    filename      text        NOT NULL,
    content_type  text        NOT NULL DEFAULT 'application/octet-stream',
    size_bytes    bigint      NOT NULL,

    -- Recorded at upload and re-checked on download by the acceptance test.
    -- This column is the difference between "the upload returned 200" and "the
    -- bytes that come back are the bytes that went in".
    sha256        bytea       NOT NULL,

    status        text        NOT NULL DEFAULT 'pending',
    uploaded_by   uuid        NOT NULL REFERENCES principals (id) ON DELETE RESTRICT,
    created_at    timestamptz NOT NULL DEFAULT now(),
    finalized_at  timestamptz,
    deleted_at    timestamptz,

    CONSTRAINT files_status_check CHECK (status IN ('pending', 'ready', 'failed')),
    CONSTRAINT files_size_nonnegative CHECK (size_bytes >= 0),
    CONSTRAINT files_sha256_length CHECK (octet_length(sha256) = 32)
);

CREATE INDEX files_org_created_idx ON files (org_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX files_uploader_idx    ON files (uploaded_by)             WHERE deleted_at IS NULL;

-- Content addressing, so re-uploading the same bytes is cheap to detect.
-- Not UNIQUE: two orgs uploading identical bytes are two files, and
-- deduplicating across a tenant boundary would leak the fact they match.
CREATE INDEX files_org_sha_idx ON files (org_id, sha256) WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS files;
