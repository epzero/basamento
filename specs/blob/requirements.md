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

A client uploads a body to a **staging key** through an **upload URL** of its
tenant, then commits the staging key: the blob store hashes the staged object
and stores it under its content key. A client never names a content key
(ADR-0013). A staging key is committed once; the commit is the authoritative
check of the size and content type bounds in both profiles, because a
provider's presigned PUT does not enforce the size.

### REQ-BLOB-021 — A fresh staging slot for every upload
When the blob store starts an upload, it shall reserve a staging key and an upload URL that are random, distinct from every earlier one and derived from no content.

### REQ-BLOB-022 — Upload options are bounds
If an upload is started with a maximum size that is not positive, a content type that is not a single `type/subtype`, a non-positive lifetime or a lifetime above the maximum blob URL lifetime, the blob store shall reject the start with code `invalid_argument`.

### REQ-BLOB-023 — A proxied upload URL is signed over its staging slot and bounds
If a request to a proxied upload URL does not carry a valid signature over the tenant, the staging key, the expiry, the maximum size and the content type, the blob store shall refuse it with HTTP status 403 and an empty body.

### REQ-BLOB-024 — A proxied upload URL writes while its staging slot is open
While the signature of a proxied upload URL is valid and its lifetime has not ended, the blob store shall accept a PUT of a body within the maximum size, with the content type bound at the start of the upload and to a staging key that is neither committed nor spent, and shall store the body as the staged object of that staging key and answer HTTP status 201 with an empty body.

### REQ-BLOB-025 — A replay replaces the staged object
When a body is uploaded again to a proxied upload URL that is still open, the blob store shall replace the staged object with that body, so that only the last body uploaded before the commit is committed.

### REQ-BLOB-026 — A spent staging key accepts no more bodies
If a request to a proxied upload URL names a staging key that was committed or spent, the blob store shall refuse it with HTTP status 403 and an empty body, and store nothing.

### REQ-BLOB-027 — A proxied upload URL expires
If a request to a proxied upload URL is made after the lifetime requested at the start of the upload has ended, the blob store shall refuse it with HTTP status 403 and an empty body, and store nothing.

### REQ-BLOB-028 — A staging key committed after its lifetime ends is not found
If a tenant commits a staging key after the lifetime requested at the start of the upload has ended, the blob store shall fail the commit with code `not_found`.

### REQ-BLOB-029 — Commit stores the body under its content key
When the tenant that started an upload commits its staging key, which is well formed, unexpired, not spent and has a staged object, the blob store shall store that staged object under the content key of its content with the content type bound at the start of the upload, delete the staged object and return that content key.

### REQ-BLOB-030 — An expired staged object is swept by the embedded blob store
When the embedded blob store sweeps the staging area under a tenant and a staging key's lifetime has ended, the blob store shall delete the staged object and the commit record of that staging key.

### REQ-BLOB-031 — A proxied upload URL enforces the size bound
If a request to a proxied upload URL carries a body over the maximum size bound at the start of the upload, the blob store shall refuse it with HTTP status 413, an empty body and no staged object.

### REQ-BLOB-032 — A proxied upload URL enforces the content type bound
If a request to a proxied upload URL carries a body whose content type is not the one bound at the start of the upload, the blob store shall refuse it with HTTP status 415, an empty body and no staged object.

### REQ-BLOB-033 — Commit enforces the size bound
If the staged object of a staging key is over the maximum size bound at the start of the upload, the blob store shall fail the commit with code `invalid_argument`, store nothing under any content key and spend the staging key.

### REQ-BLOB-034 — Commit enforces the content type bound
If the staged object of a staging key has a content type other than the one bound at the start of the upload, the blob store shall fail the commit with code `invalid_argument`, store nothing under any content key and spend the staging key.

### REQ-BLOB-035 — A staging key belongs to its tenant
If a tenant commits a staging key that another tenant started the upload for, the blob store shall fail the commit with code `not_found`, and shall keep the staged object and the content keys of both tenants untouched.

### REQ-BLOB-036 — Staging keys are distinct across tenants
When two tenants each start an upload, the blob store shall reserve two staging keys that differ.

### REQ-BLOB-037 — A staging key never issued is not found
If a tenant commits a well-formed staging key that no upload of that tenant issued, the blob store shall fail the commit with code `not_found`.

### REQ-BLOB-038 — A staging key without a staged object is not found
If a tenant commits a well-formed staging key whose staged object was never uploaded, the blob store shall fail the commit with code `not_found`.

### REQ-BLOB-039 — A staging key is committed once
If a tenant commits a staging key that was committed before, the blob store shall fail the commit with code `not_found` and leave the blob the earlier commit stored unchanged.

### REQ-BLOB-040 — A failed commit spends the staging key
When the staged object bound to a staging key that was well formed, unexpired and not spent is over the maximum size or of another content type than the one bound at the start of the upload, the blob store shall delete the staged object and record the staging key as spent when the commit fails, so that a later commit of the key fails with code `not_found`.

### REQ-BLOB-041 — Malformed staging keys
If a commit names a key that is not a well-formed staging key, the blob store shall reject it with code `invalid_argument` and touch no stored data.

### REQ-BLOB-042 — Uploads need a tenant
If the blob store starts an upload, or commits a staging key, for a caller with no tenant, the blob store shall reject the call with code `invalid_argument` and touch no stored data.

### REQ-BLOB-043 — A content key that already holds a blob is kept
When a commit stores content under a content key that already holds a blob of the committing tenant, the blob store shall keep the existing blob and its content type unchanged.
