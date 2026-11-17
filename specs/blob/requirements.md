# Blob: content-addressed blob storage

Status: **Planned** (WP-L1-02). Design: ADR-0004, [`design.md`](design.md).

The blob store keeps immutable content (assets, large run payloads, browser
recordings) under its content key, separately for each tenant. The embedded
profile keeps blobs in a local directory and hands out proxied blob URLs; the
cloud profile keeps them in S3, GCS or Azure. Both implementations pass the
same scenarios.

### REQ-BLOB-001 — Content keys
The blob store shall identify each blob by its content key: `sha256:` followed by the 64 lowercase hexadecimal digits of the SHA-256 hash of its content.

### REQ-BLOB-002 — Content must match its key
If the content written under a content key does not hash to that key, then the blob store shall reject the write with code `invalid_argument` and keep nothing of that content.

### REQ-BLOB-003 — Malformed keys
If a call names a key that is not a well-formed content key, then the blob store shall reject the call with code `invalid_argument` and touch no stored data.

### REQ-BLOB-004 — Unchanged content
When a blob is read, the blob store shall return exactly the bytes that were written under its content key.

### REQ-BLOB-005 — Writes are idempotent
When the same content is written under a content key that already holds a blob of the same tenant, the blob store shall succeed and keep the existing blob and its content type unchanged.

### REQ-BLOB-006 — Missing blobs
If a read, a deletion or a blob URL request names a content key under which the calling tenant has no blob, then the blob store shall fail the call with code `not_found`.

### REQ-BLOB-007 — Existence
When asked whether a content key exists, the blob store shall answer true exactly when the calling tenant has a blob under that key.

### REQ-BLOB-008 — Deletion
When a blob is deleted, the blob store shall no longer return it to any read, existence check or blob URL of the deleting tenant.

### REQ-BLOB-009 — No partial blobs
If a write ends before all of its content is stored, because the source fails or the call is cancelled, then the blob store shall keep nothing under that content key.

### REQ-BLOB-010 — Tenant isolation
The blob store shall scope every blob to the tenant of the call that wrote it, so that no call of another tenant can read it, learn that it exists, delete it or get a blob URL for it.

### REQ-BLOB-011 — Tenant required
If a call carries no tenant, then the blob store shall reject it with code `invalid_argument` and touch no stored data.

### REQ-BLOB-012 — Durable across restarts
When the blob store is opened again on the same location, the blob store shall return every blob that was stored and not deleted before it closed.

### REQ-BLOB-013 — Blob URLs serve the content
While a blob URL is within its lifetime, the blob store shall serve the blob's bytes with its content type to any holder of the URL, without other credentials.

### REQ-BLOB-014 — Default content type
When a blob was written without a content type, the blob store shall serve it with content type `application/octet-stream`.

### REQ-BLOB-015 — Blob URLs expire
If a proxied blob URL is used after its lifetime has ended, then the blob store shall refuse it with HTTP status 403 and serve none of the content.

### REQ-BLOB-016 — Blob URLs cannot be altered
If a proxied blob URL has been altered or carries no valid signature, then the blob store shall refuse it with HTTP status 403 and serve none of the content.

### REQ-BLOB-017 — Blob URL lifetimes are bounded
If a blob URL is requested with a lifetime that is not positive or exceeds the maximum blob URL lifetime, then the blob store shall reject the request with code `invalid_argument`.

### REQ-BLOB-018 — Opened by URL
When a blob store is opened from a location URL, the blob store shall select the implementation by the URL scheme: `file` for a local directory, `s3`, `gs` or `azblob` for the cloud.

### REQ-BLOB-019 — Unsupported locations
If a location URL has no scheme or a scheme the blob store does not support, then opening it shall fail with code `invalid_argument` and a message that names the supported schemes.

### REQ-BLOB-020 — Empty data directory
When a blob store is opened on a local directory that does not exist yet, the blob store shall create the directory and start with no blobs.
