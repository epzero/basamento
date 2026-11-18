# Blob: design

Decisions the implementer of WP-L1-02 (`platform/blob`) follows. The port is
`platform.Blob` in `platform/platform.go`; its method set stays as it is for
this work package. Uploads are added by WP-L1-08 (ADR-0013).

## Content keys

- A content key is `sha256:` and 64 lowercase hex digits. Nothing else is a
  key: no other prefix, no uppercase, no path characters (REQ-BLOB-001,
  REQ-BLOB-003). Keys are validated before any storage call.
- `Put(ctx, key, r, contentType)` hashes the content while it streams it to
  the bucket and commits only when the hash equals `key`. On a mismatch, a
  source error or a cancelled context it aborts the `gocloud.dev/blob` writer
  (cancel its context before `Close`), so no object and no temporary file
  remain (REQ-BLOB-002, REQ-BLOB-009).
- A `Put` for a key the tenant already holds still hashes the reader so a
  content mismatch fails with `invalid_argument` without touching the existing
  blob (REQ-BLOB-002); when the hash matches, `Put` succeeds without rewriting
  the object and the first content type stays (REQ-BLOB-005). An empty content
  type is stored as `application/octet-stream` (REQ-BLOB-014).

## Tenancy

- The tenant comes from `ctx`, as for `Credentials`. A call without one fails
  with `invalid_argument` before touching the bucket (REQ-BLOB-011).
- Objects live under `<tenant id>/<64 hex digits>` in one bucket. There is no
  cross-tenant deduplication: sharing would let one tenant learn through
  `Exists` that another holds some content (REQ-BLOB-010).
- Every miss, including another tenant's blob, is `not_found` (REQ-BLOB-006).

## Blob URLs

- `SignedURL(ctx, key, ttl)` checks the key, that the tenant holds the blob,
  and `0 < ttl <= max`, where the maximum blob URL lifetime is configured and
  is 7 days by default, the longest an S3 presigned URL may live
  (REQ-BLOB-017).
- **Cloud:** the provider's signed URL (`bucket.SignedURL`); the provider
  enforces expiry, so the expiry and tampering scenarios are
  `@profile:embedded`.
- **Embedded:** a proxied blob URL, `<base URL>/<tenant>/<64 hex>?exp=<unix
  seconds>&sig=<HMAC-SHA256 over tenant, key and expiry>`, served by an
  `http.Handler` exported by `platform/blob` that `kit serve` mounts
  (WP-L1-05). The handler compares the signature in constant time, reads the
  expiry against the injected `core` clock (valid while now < expiry), and
  answers 403 with an empty body for any failure and 404 for a deleted blob
  (REQ-BLOB-013, REQ-BLOB-015, REQ-BLOB-016). The signing key and base URL
  are constructor arguments; wiring chooses them.

## Opening

- `Open(ctx, url, opts)` selects by scheme through `gocloud.dev/blob` URL
  openers: `file` (fileblob, creating the directory with mode 0700 if
  missing), `s3`, `gs`, `azblob`. Any other scheme, or no scheme, fails with
  `invalid_argument` naming the four schemes (REQ-BLOB-018 to REQ-BLOB-020).
  `mem` is not supported: production code holds no test doubles.

## Conformance

One conformance suite in `platform/blob/conformance` runs every scenario
without a `@profile` tag against fileblob in a temporary directory and against
`s3blob` pointed at an in-process S3-compatible simulator (`docs/engineering/qa-gate.md`
G3). No scenario in the gate reaches a live bucket; gs and azblob are proven
by the `@live` scenario nightly.
