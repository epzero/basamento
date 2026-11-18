# Blob: content-addressed blob storage

Status: **Planned** (WP-L1-02, WP-L1-08). Design: ADR-0004, ADR-0013, [`design.md`](design.md).

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

## Uploads

A client uploads a body to a **staging key** through a single-use **upload
URL** of its tenant, then commits the staging key: the blob store hashes the
staged object and stores it under its content key. A client never names a
content key (ADR-0013).

### REQ-BLOB-021 — Commit stores the body under its content key
When a tenant commits a staging key whose staged object is present, then the blob store shall store the staged object under the content key of its content with the content type bound at `StartUpload` and return that content key.

### REQ-BLOB-022 — A fresh staging slot for every upload
When a tenant starts an upload, then the blob store shall reserve a new staging key and an upload URL that are random, distinct from every earlier one and derived from no content.

### REQ-BLOB-023 — Upload options are bounds
If an upload is started with a maximum size that is not positive, a content type that is not a single `type/subtype`, a non-positive lifetime or a lifetime above the maximum blob URL lifetime, then the blob store shall reject the start with code `invalid_argument`.

### REQ-BLOB-024 — Upload URLs are signed, single use and scoped to their bounds
When an upload URL is used, then the blob store shall accept the body only if it carries a valid signature over the tenant, the staging key, the expiry, the maximum size and the content type, has not been used before, starts no later than its expiry and fits within the bound's maximum size and content type, and shall otherwise refuse it with status 403 for a bad signature or expiry, 413 over the maximum size and 415 for the wrong content type without storing any of the body.

### REQ-BLOB-025 — Upload URLs and staging keys expire
If an upload URL is used after the lifetime requested at `StartUpload` has ended, then the blob store shall refuse it with status 403 and store no body; and if a staging key is committed after that lifetime has ended, then the blob store shall fail the commit with code `not_found`.

### REQ-BLOB-026 — Uncommitted objects expire
When a staging key's lifetime has ended and the blob store sweeps expired staged objects, then the blob store shall delete the staging object and keep the staging key from being committed.

### REQ-BLOB-027 — The size bound is enforced at the URL
If the body sent to an upload URL exceeds the maximum size of the request, then the blob store shall refuse it with status 413 and store no staged object.

### REQ-BLOB-028 — The content type bound is enforced at the URL
If the body sent to an upload URL does not have the content type bound at `StartUpload`, then the blob store shall refuse it with status 415 and store no staged object.

### REQ-BLOB-029 — Staged objects are scoped to their tenant
The blob store shall refuse a commit by a tenant other than the one that started the upload with code `not_found` and store nothing under that tenant's content keys, and shall name each staging key to one tenant only.

### REQ-BLOB-030 — Unknown staging keys
If a commit names a staging key the calling tenant holds no uncommitted staging object for, because the key was never issued, its staged object was never uploaded, was removed by a sweep or deleted by a failed commit, then the blob store shall fail the commit with code `not_found`.

### REQ-BLOB-031 — A staging key is committed once
If a commit names a staging key that was committed before, then the blob store shall fail it with code `not_found` and leave the blob the earlier commit stored unchanged.

### REQ-BLOB-032 — A failed commit stores nothing
If a commit finds its staged object over the maximum size or of another content type than the one bound at `StartUpload`, then the blob store shall fail it with code `invalid_argument`, store nothing under any content key and leave the staging key committed: a later commit of it fails with code `not_found`.

### REQ-BLOB-033 — Malformed staging keys
If a commit names a key that is not a well-formed staging key, then the blob store shall reject it with code `invalid_argument` and touch no stored data.

### REQ-BLOB-034 — Uploads need a tenant
If an upload is started, or a staging key is committed, without a tenant in its context, then the blob store shall reject the call with code `invalid_argument`.
