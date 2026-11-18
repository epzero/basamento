# Blob: design

Decisions the implementers of WP-L1-02 and WP-L1-08 (`platform/blob`) follow.
The port is `platform.Blob` in `platform/platform.go`: WP-L1-02 built the store
and the read URLs, and WP-L1-08 adds the upload methods of §Uploads.

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

## Uploads (WP-L1-08)

Uploads stage first and commit second (ADR-0013): the client never names a
content key, and only `Commit` writes one.

### The port

Two methods are added to `platform.Blob`, additively; `Put`, `Get`, `Exists`,
`Delete` and `SignedURL` are unchanged.

```go
StartUpload(ctx context.Context, o UploadOptions) (Upload, error)
Commit(ctx context.Context, stagingKey string) (key string, err error)

// UploadOptions are what a client is allowed to do with an upload URL.
// Every field is a bound: the staged object must meet all of them, or
// Commit refuses it.
type UploadOptions struct {
	MaxSize     int64         // bytes, > 0
	ContentType string        // one type/subtype, no list, no wildcard
	TTL         time.Duration // 0 < TTL <= the maximum blob URL lifetime
}

// Upload is one staging slot.
type Upload struct {
	URL        string    // where the client PUTs the body
	StagingKey string    // opaque, single use, refused once committed
	Expires    time.Time // now + TTL, from core.Clock
}
```

`Blob` also gains, outside the two methods, one embedding the profile's own
handler needs to see. An interface value built by `platform/blob` offers it
when its profile serves proxied URLs:

```go
// UploadHandler answers a request to a proxied upload URL. It implements
// http.Handler; kit serve mounts it next to the proxied blob URL handler.
// It is present only in the embedded profile.
UploadHandler() http.Handler
```

Wiring keeps that out of `platform.Blob` itself so the cloud store answers a
proxied request as it would answer a request the platform has no credentials
for: refused, no body stored.

### Staging keys and staging objects

- A staging key is opaque to the client and made of characters that need no
  escaping in a path segment: 32 random bytes from `crypto/rand` in unpadded
  base64url (43 characters), prefixed with `stage_`:
  `stage_<43 base64url chars>`. It is random, never derived from content, the
  options or the tenant, so two uploads never share one (REQ-BLOB-022), and
  neither does the same upload started twice.
- A value that does not match `^stage_[A-Za-z0-9_-]{43}$` is not a staging
  key: `Commit` rejects it with `invalid_argument` before any storage call and
  leaves the store untouched (REQ-BLOB-033).
- **Staging object key:** `<tenant id>/stage/<staging key>`. It is one path
  segment after the tenant's directory, so no tenant can name another's
  staging object, and it cannot collide with a content key, which is 64 hex
  digits (REQ-BLOB-029). There is no cross-tenant lookup, exactly as for
  content keys (REQ-BLOB-010).
- **Marker:** committing a staging key writes, in the same bucket, the
  **commit record** `<tenant id>/stage/<staging key>.committed`, with an empty
  body, after the content key exists and before the staging object is deleted.
  A staging key with a commit record is spent: `Commit` answers `not_found`
  for it (REQ-BLOB-031), and no upload URL writes to it again (REQ-BLOB-024).
  The record's expiry is the one bound at `StartUpload`, so the sweep removes
  it and deleted content can be committed again under its key, as `Put` does
  after a deletion.
- The bound options live only in the upload URL (embedded) or the presigned
  URL (cloud); `Commit` needs them from the slot, so the embedded store keeps
  them in memory (keyed by tenant and staging key, with the expiry) for the
  slots it issued, and the cloud store passes the content type to the
  provider's `Copy` and relies on the presigned PUT for size and type. A slot
  the embedded store no longer knows is `not_found`.

### Commit

Under the tenant of `ctx`, in this order, with no fallback to another tenant:

1. no tenant in `ctx` → `invalid_argument` (REQ-BLOB-034); a malformed
   staging key → `invalid_argument` (REQ-BLOB-033);
2. a commit record, an unknown slot or `now >= Expires` → `not_found`
   (REQ-BLOB-025, REQ-BLOB-030, REQ-BLOB-031);
3. `Get` of the staging object; a miss → `not_found`; a content type other
   than the bound one → `invalid_argument` (REQ-BLOB-028, REQ-BLOB-032);
4. stream the object through a `sha256` hasher and an `io.LimitReader` of
   `MaxSize+1` bytes: one more byte than the bound → `invalid_argument`
   (REQ-BLOB-027, REQ-BLOB-032);
5. `Put(ctx, "sha256:"+hex, <a reopening reader of the staged object>,
   bound content type)`: WP-L1-02's write, so an existing blob keeps its
   content type (REQ-BLOB-005);
6. write the commit record; `Delete` the staging object; return the content
   key (REQ-BLOB-021).

A failure at any step after 1 deletes the staging object and writes the
commit record, in that order, so a failed commit is spent and leaves nothing
readable (REQ-BLOB-032). Step 5 aborts before writing anything when its
reader fails, as `Put` does (REQ-BLOB-009). A store error is mapped to
`unavailable`; the staging object is kept then, so the client may retry
`Commit` until its TTL.

### Embedded signer

The proxied upload URL is served by `platform/blob` and signed with the same
signing key as the proxied blob URLs:

`<base URL>/blob/upload/<tenant>/<staging key>?exp=<unix>&max=<bytes>&ct=<type>&sig=<signature>`

- The read URL's `<signature>` is `HMAC-SHA256(key, tenant, key, exp)`. The
  upload URL's is `HMAC-SHA256(key, "upload", tenant, staging key, exp, max,
  content type)`: the same key, the same hex encoding, the same `exp` rule
  (valid while now < exp), and a field list with a distinct first component so
  neither URL's signature is valid on the other kind. `ct` is the content
  type percent-encoded.
- The upload handler checks, in this order: the signature in constant time →
  403; the expiry against the injected `core.Clock` → 403; `Content-Length`
  over `max` (or absent) → 413; `Content-Type` other than the signed one →
  415; a staging key the store does not know for this tenant → 404; a body
  over `max` while it streams (a chunked request) → 413 with nothing stored;
  otherwise it writes the body to the staging object and answers 201.
  Every refusal returns an empty body and stores nothing.
- A URL is single use because its staging key is: once the key is committed,
  or its slot has expired, the handler answers 403, as it does for a key that
  does not exist. A replayed PUT to a live, uncommitted URL replaces the
  staged object; only the last body before `Commit` matters.

### Cloud presign

`StartUpload` on the S3, GCS or Azure store opens the bucket through
`gocloud.dev/blob` and calls `bucket.SignedURL(<tenant>/stage/<staging key>,
&blob.SignedURLOptions{Method: "PUT", Expiry: TTL, ContentType: o.ContentType})`;
the size bound is a `Content-Length` condition on the presigned PUT
(`WithConditions` with `IfNotExists()` on S3). The provider enforces expiry,
signature, size and type; `Commit` enforces them again, as it does for the
embedded profile.

### Expiry sweep

- **Embedded:** expired staging objects and commit records are swept on open
  and then at most once per maximum TTL interval, from the `core.Clock`,
  never a `time.Sleep` or the wall clock: `<tenant>/stage/` is listed, and
  every object whose bound expiry is at or before now is deleted
  (REQ-BLOB-026). The in-memory slots are dropped with them.
- **Cloud:** a bucket lifecycle rule on the staging prefix expires objects
  after 7 days, the maximum TTL; `Commit` refuses a late key anyway.

### Errors

| Situation | `StartUpload` | `Commit` | Upload URL (embedded) |
| --- | --- | --- | --- |
| no tenant in `ctx` | `invalid_argument` | `invalid_argument` | — |
| bad option: `MaxSize <= 0`, a content type that is not one `type/subtype`, `TTL <= 0` or `> max` | `invalid_argument` | — | — |
| malformed staging key | — | `invalid_argument` | 403 |
| unknown, other tenant's, expired or committed staging key | — | `not_found` | 403 |
| bad signature or expired URL | — | — | 403 |
| body over `MaxSize` | — | `invalid_argument` | 413 |
| wrong content type | — | `invalid_argument` | 415 |
| staging object missing | — | `not_found` | 404 |
| store or provider fails | `unavailable` | `unavailable` | 503 |
| success | `Upload` | content key | 201, empty body |

There is no `conflict`, and no hash mismatch: a client cannot name a hash, so
`Commit` has nothing to compare it with (ADR-0013).

### Conformance

`uploads.feature` runs, like `storage.feature`, against fileblob and against
`s3blob` on the in-process S3 simulator for every scenario without
`@profile`. Tampering, expiry and tenant substitution are
`@profile:embedded`, as for read URLs; the cloud profile's enforcement of them
is the provider's and is proven by an `@live` nightly run.
