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
- **Write failures carry their class and leave nothing** (REQ-BLOB-046,
  REQ-BLOB-047). A reader that fails is `invalid_argument`; a cancelled
  context is `cancelled`, checked first, so a source that fails because the
  caller cancelled reads as `cancelled`. A failure of the bucket or of the
  writer is `unavailable`, marked retryable. A failed write keeps nothing
  under the tenant's content key: no object, partial or whole, and no content
  type sidecar the write created.
- `key` is the expected hash: `Put` has no other hash input, so a mismatch is
  the only hash check, and `Commit` passes `sha256:` of what it read
  (ADR-0013).
- The body reaches the bucket through one writer: `Put` streams the reader
  once into the object under its content key, hashing it on the way, and
  commits it only when the hash matches and `Close` succeeds. On every
  failure it cancels the writer's context before `Close`, so nothing is
  committed; any staging or sidecar the write created is cleaned up with a
  context independent of the caller's, so a cancelled caller still cleans up.
  No failure path deletes or truncates an existing complete object, of this
  tenant's, another tenant's or another key's. A store failure mid-write
  leaves nothing, and a retried `Put` re-reads and rewrites.
  `TestPutWriterFailMidStreamUnavailable` covers the `unavailable` class; it
  and `TestPutFailMidStreamNoObjectFile` assert that a failed write leaves no
  object or sidecar and that the retry succeeds.
- A `Put` for a key the tenant already holds still hashes the reader so a
  content mismatch fails with `invalid_argument` without touching the existing
  blob (REQ-BLOB-002); when the hash matches, `Put` succeeds without rewriting
  the object and the first content type stays (REQ-BLOB-005). An empty content
  type is stored as `application/octet-stream` (REQ-BLOB-014).

## Tenancy

- The tenant comes from `ctx`, as for `Credentials`: every exported method and
  the proxied URL handler's reads take it from the context with
  `core.TenantFrom` and never from an argument (REQ-BLOB-006, REQ-BLOB-011). A
  call without one fails with `invalid_argument` before touching the bucket
  (REQ-BLOB-011), and the handler builds its context with `core.WithTenant`
  from the tenant it verified in the URL.
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
  are `Open` options; wiring chooses them.
- **Tenant in the URL.** The tenant segment is `url.PathEscape`d. Interior
  characters such as `%`, `?`, `#`, space and non-ASCII letters are valid
  tenant characters under REQ-CORE-005, and `url.PathEscape` changes each of
  them: `a b` becomes `a%20b`, `a?b` `a%3Fb`, `a#b` `a%23b`, `a%b` `a%25b` and
  `aéb` `a%C3%A9b`. The escaping therefore keeps the tenant one path segment
  and out of the query. The handler reads the decoded tenant back from the
  request path, the same value `TenantFrom` would give, and the HMAC covers
  that decoded tenant, so the round trip is exact.
- **Protective headers.** The application serves proxied blob URLs from its own
  origin, where a script stored by a tenant as `text/html` or `image/svg+xml`
  would otherwise run as a page of the application: stored XSS
  (REQ-BLOB-044). A served response therefore carries
  `Content-Disposition: attachment; filename="<hex>"`,
  `Content-Security-Policy: sandbox; default-src 'none'`,
  `Cache-Control: private, no-store` and `X-Content-Type-Options: nosniff`,
  and keeps the stored `Content-Type`. A refused response carries an empty
  body, none of the blob's content and no stored content type; it still
  carries `Cache-Control: private, no-store` and
  `X-Content-Type-Options: nosniff`, which name nothing of the content. A blob
  of tenant-chosen content that is an attachment with a script-disabled
  sandbox cannot run on that origin. A dedicated origin for blobs, where a
  page escapes nothing of the application, is the alternative; it adds an
  origin, a certificate and a routing rule per environment to a URL the
  handler already serves, so it is not chosen.
- **Signing key** (REQ-BLOB-045). Where the profile is embedded, a non-empty
  `SignKey` under 32 bytes, the HMAC-SHA256 digest size, is refused at `Open`
  with `invalid_argument`; the cloud store uses no signing key. An empty
  `SignKey` disables proxied URLs: `SignedURL` on the embedded store fails
  with `internal`, and the handler answers 403 to every request. `BaseURL` is
  required the same way: `SignedURL` fails with `internal` without it. The
  cloud store signs with the provider's credentials and ignores both.

## Opening

- `Open(ctx, url, opts)` selects by scheme through `gocloud.dev/blob` URL
  openers: `file` (fileblob, creating the directory with mode 0700 if
  missing), `s3`, `gs`, `azblob`. Any other scheme, or no scheme, fails with
  `invalid_argument` naming the four schemes (REQ-BLOB-018 to REQ-BLOB-020).
  `mem` is not supported: production code holds no test doubles.
- **Embedded profile.** A `file` URL is opened with fileblob's `no_tmp_dir`
  option, unless the operator set it in the URL. By default fileblob stages a
  write in the system temporary directory and renames it into place, which
  fails when the data directory is a mount of its own; `no_tmp_dir` creates
  the temporary file next to the target object instead (`<object>.<hex>.tmp`),
  and fileblob renames it into place on `Close`. A process killed mid-write
  can leave those files behind: an operator may remove them when no write is
  in flight. They are never read as blobs, because a blob is named by its
  64-hex object path.

## Content type

- The content type lives in a sidecar object `<tenant id>/<64 hex>.meta`,
  holding the type as its body. The embedded store needs it anyway, because
  fileblob keeps no attributes; both profiles use it, so one read path serves
  both. A missing sidecar, from a blob written before it existed or by a
  cloud tool, reads as no stored type, and the response carries
  `application/octet-stream` (REQ-BLOB-014).
- The sidecar is written before the object, and the object's existence is
  the commit point: a reader that finds the object finds its type. A returned
  failure removes the sidecar the write created (REQ-BLOB-047); a crash
  between the two can leave an orphan sidecar, which the next write
  overwrites.
- **Concurrency.** Within one process, `Put` serializes writes of one key
  under one tenant, so the first content type wins (REQ-BLOB-005,
  REQ-BLOB-043). Across replicas there is no such lock: two writers that both
  see a new key can each write a sidecar before either writes the object, and
  the later sidecar's type stays. The bytes are identical, by content
  addressing, and the first content type wins only within a replica. A
  caller must not rely on first-wins across replicas; the race is accepted.

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
	StagingKey string    // opaque; one commit, then spent
	Expires    time.Time // now + TTL, from core.Clock
}
```

`platform/blob` also exports a handler type for the embedded profile:

```go
// UploadHandler answers a request to a proxied upload URL. It implements
// http.Handler; kit serve mounts it next to the proxied blob URL handler.
// Only the embedded store offers one: only its URLs are proxied.
type UploadHandler http.Handler
```

The cloud store implements `platform.Blob` without it, because its URLs are
the provider's. The constructor of the embedded store returns both the store
and its `UploadHandler`. `http.Handler` is not added to `platform.Blob`, so
the cloud store needs no stub for a request it never receives.

### Staging keys and staged objects

- A staging key is opaque to the client and made of characters that need no
  escaping in a path segment: 32 random bytes from `crypto/rand` in unpadded
  base64url (43 characters), prefixed with `stage_`:
  `stage_<43 base64url chars>`. It is random, never derived from content, the
  options or the tenant, so two uploads never share one, across tenants too
  (REQ-BLOB-021, REQ-BLOB-036), and neither does the same upload started
  twice.
- A value that does not match `^stage_[A-Za-z0-9_-]{43}$` is not a staging
  key: `Commit` rejects it with `invalid_argument` before any storage call and
  leaves the store untouched (REQ-BLOB-041).
- **Staging object key:** `<tenant id>/stage/<staging key>`. It is one path
  segment after the tenant's directory, so no tenant can name another's
  staging object, and it cannot collide with a content key, which is 64 hex
  digits. There is no cross-tenant lookup, exactly as for content keys
  (REQ-BLOB-010); a commit under a tenant that did not start the upload is
  `not_found` and touches neither tenant's staging object (REQ-BLOB-035).
- **Slot metadata:** `<tenant id>/stage/<staging key>.slot`, an object whose
  body is the bound options as JSON (`max`, `content_type`, `expires`). Both
  profiles write it in `StartUpload` before they sign or presign the URL, and
  `Commit` reads it: the bounds and the expiry are never kept in memory or
  read from the URL, so they survive a restart of the embedded store and a
  replica that did not issue the URL. The slot is open while its metadata
  exists and its expiry has not passed. It lives until the sweep deletes it.
- **Commit record:** `<tenant id>/stage/<staging key>.committed`, with an
  empty body. It is internal to the store: clients never see it, and no
  scenario asserts it. A staging key with a commit record is spent: `Commit` answers
  `not_found` for it (REQ-BLOB-039), and a proxied upload URL for it answers
  403 (REQ-BLOB-026). The record is written only by a successful commit
  (step 7 below) and by a failed one that the size or the content type bound
  refused (step 4 or 5 below, REQ-BLOB-033, REQ-BLOB-034, REQ-BLOB-040); in that
  failure the staged object is deleted first. A commit that answers
  `not_found` (step 2 or 3 below: no slot metadata, an expired slot, another
  tenant's, a key no upload issued, or no staged object yet) writes nothing
  and deletes nothing, so no caller can create a record for a key it did not
  receive.

### Commit

A commit is the authoritative check of the bounds in both profiles. A
presigned PUT cannot enforce the size (`MaxSize`), cannot answer 413 or 415
with an empty body, and a commit cannot revoke it; the provider enforces the
expiry and the signature, and may or may not enforce the content type header
(S3 signs it, GCS and Azure leave it open). The scenarios of a commit failure (a staged object over the maximum size or of another content
type, nothing stored, the key spent) run in both profiles and carry no
`@profile` tag.

Under the tenant of `ctx`, in this order, with no fallback to another tenant:

1. no tenant in `ctx` → `invalid_argument` (REQ-BLOB-042); a malformed
   staging key → `invalid_argument` (REQ-BLOB-041);
2. no slot metadata, a commit record, or `now >= expires` → `not_found`
   (REQ-BLOB-028, REQ-BLOB-037, REQ-BLOB-039), nothing written or deleted;
3. `Get` of the staging object; a miss → `not_found` (REQ-BLOB-038), and the
   slot stays open, so the client may upload the body afterwards and commit
   the same key;
4. its content type other than the bound one → `invalid_argument`
   (REQ-BLOB-034);
5. stream the object through a `sha256` hasher and an `io.LimitReader` of
   `MaxSize+1` bytes: one more byte than the bound → `invalid_argument`
   (REQ-BLOB-033);
6. `Put(ctx, "sha256:"+hex, <a reopening reader of the staged object>,
   bound content type)`: WP-L1-02's write, so an existing blob keeps its
   content type (REQ-BLOB-005, REQ-BLOB-043);
7. write the commit record; `Delete` the staged object; return the content
   key (REQ-BLOB-029). The slot metadata stays until the sweep.

An `invalid_argument` at steps 4 or 5 deletes the staging object and writes
the commit record, in that order, so a failed commit is spent and leaves
nothing readable (REQ-BLOB-040). A crash between the two leaves the key not
yet spent with its staged object gone, so a retried commit is `not_found`
(REQ-BLOB-038). Step 6 aborts before writing anything when
its reader fails, as `Put` does (REQ-BLOB-009). A store error is mapped to
`unavailable`; the staging object and the slot metadata are kept then, so the
client may retry `Commit` until the expiry. `Platform/blob`'s conformance
tests build an over-sized or mis-typed staged object with the store's own
conformance writer, so the test double stays in the test package.

### Embedded signer

The proxied upload URL is served by the embedded store's `UploadHandler` and
signed with the same signing key as the proxied blob URLs:

`<base URL>/blob/upload/<tenant>/<staging key>?exp=<unix>&max=<bytes>&ct=<type>&sig=<signature>`

- Every proxied upload URL carries all four parameters. One that is missing
  any of them, or whose `sig` does not verify, is refused with 403.
- The upload URL's signature is `HMAC-SHA256(key, "upload", tenant, staging
  key, exp, max, content type)`: the same key, the same hex encoding and the
  same `exp` rule (valid while now < exp) as the read URL, whose signature is
  `HMAC-SHA256(key, tenant, key, exp)`. It covers the expiry, the maximum size
  and the content type, and the distinct first component makes neither URL's
  signature valid on the other kind of URL or on another staging key
  (REQ-BLOB-023). `ct` is the content type percent-encoded.
- The handler checks, in this order: the four parameters are present → else
  403; the signature in constant time → 403; the expiry against the injected
  `core.Clock` → 403; no slot metadata (a key no upload issued, another
  tenant's, or a swept slot), a commit record, or an expired slot → 403; the
  signed `max` and `ct` differ from the slot metadata → 403;
  `Content-Length` over the bound `MaxSize` → 413; `Content-Type` other than
  the bound one → 415; a body over `MaxSize` while it streams (a chunked
  request, so no `Content-Length`) → 413 with nothing stored; otherwise it
  writes the body to the staging object and answers 201 with an empty body. A
  slot that is open and has no staged object accepts its first PUT. Every
  refusal returns an empty body and stores nothing.
- A proxied upload URL writes while its signature is valid, its slot has not
  expired and its staging key is neither committed nor spent (REQ-BLOB-024).
  A replayed PUT to such a URL replaces the staged object, so a client can
  retry an interrupted upload; only the last body before `Commit` counts
  (REQ-BLOB-025). A committed or spent key answers 403 (REQ-BLOB-026, BLOB-026-01 to
  BLOB-026-03): the
  "single use" is one commit per staging key.

### Cloud presign

`StartUpload` on the S3, GCS or Azure store writes the slot metadata and opens
the bucket through `gocloud.dev/blob`, then calls
`bucket.SignedURL(ctx, <tenant>/stage/<staging key>,
&blob.SignedURLOptions{Method: http.MethodPut, Expiry: o.TTL, ContentType:
o.ContentType})`: the provider enforces the expiry and the signature, and
signs the content type. No condition on the size is set (`IfNotExists()` is dropped: it
expresses existence, not a bound). The size is enforced by `Commit` only, and
the bucket lifecycle rule below expires abandoned slots.
- **Abandoned multipart uploads, an operator requirement.** A presigned PUT
  large enough to be multipart leaves its parts in the bucket until the
  upload is completed or aborted. The operator configures an
  `AbortIncompleteMultipartUpload` lifecycle rule on the bucket: abort
  incomplete multipart uploads after 1 day, S3 and GCS alike (GCS aborts them
  through the same `AbortIncompleteMultipartUpload` action of its lifecycle
  rule). The staging rule below deletes the objects; this rule deletes the
  parts an interrupted PUT left behind.

### Expiry sweep

- **Embedded, REQ-BLOB-030:** expired staged objects, slot metadata and commit
  records are swept on open and then at most once per maximum TTL interval,
  from the `core.Clock`, never a `time.Sleep` or the wall clock. Each
  tenant's `<tenant>/stage/` is listed once per sweep, and every staging key
  whose slot metadata says its expiry is at or before now is swept. The sweep
  deletes a slot's staged object and commit record first, and its slot
  metadata last. Commit follows the same order: it writes the commit record
  before it deletes the staged object, and a failed commit deletes the staged
  object before it writes the commit record. A crash at any point therefore
  leaves the slot metadata in place, and the next sweep finishes the job; no
  object under `stage/` outlives its slot's expiry by more than one sweep
  interval. Once its commit record is swept, a spent key is only an unknown
  key: `Commit` still answers `not_found` for it (REQ-BLOB-037).
- **Cloud:** the sweep is the bucket's lifecycle rule on the staging prefix,
  which expires every object there after 7 days, the maximum TTL. The
  embedded sweep is not part of the cloud store; `Commit` refuses a late key
  from the slot metadata even before the rule acts. A key whose slot metadata
  the rule removed is an unknown key: `not_found`. The abort rule above is
  configured by the same operator step.

### Errors

| Situation | `StartUpload` | `Commit` | Proxied upload URL |
| --- | --- | --- | --- |
| no tenant in `ctx` | `invalid_argument` | `invalid_argument` | — |
| bad option: `MaxSize <= 0`, a content type that is not one `type/subtype`, `TTL <= 0` or `> max` | `invalid_argument` | — | — |
| a key that is not a staging key | — | `invalid_argument` | 403 |
| a key no slot metadata covers: never issued by the tenant or by an expired or swept slot, or of another tenant | — | `not_found` | 403 |
| an open slot with no staged object | — | `not_found` (the slot stays open) | 201, the PUT writes it |
| an expired slot | — | `not_found` | 403 |
| a commit record: committed or spent | — | `not_found` | 403 |
| a URL missing `exp`, `max`, `ct` or `sig`, a bad signature, or an expired URL | — | — | 403 |
| the signed `max` or `ct` differs from the slot metadata | — | — | 403 |
| body over `MaxSize` | — | — | 413 (embedded; the cloud PUT is not checked, and `Commit` refuses the object) |
| body of another content type | — | — | 415 (embedded; S3 refuses the cloud PUT, and `Commit` refuses the object where the provider does not) |
| staged object over `MaxSize` | — | `invalid_argument` | — |
| staged object of another content type | — | `invalid_argument` | — |
| store or provider fails | `unavailable` | `unavailable` | 503 |
| success | `Upload` | content key | 201, empty body |

Every refusal of a proxied upload URL has an empty body. The two staged-object
rows are checked in both profiles by `Commit`; the URL rows are URL-level and
therefore `@profile:embedded`. There is no `conflict` and no
hash mismatch: a client cannot name a hash, so `Commit` has nothing to compare
it with (ADR-0013); `Put` does, and is the only call with a hash mismatch.

### Conformance

`uploads.feature` runs, like `storage.feature`, against fileblob and against
`s3blob` on the in-process S3 simulator for every scenario without
`@profile`: the start, the commit, its bounds, its tenancy and its failures.
The URL-level behaviour (signature, tampering, tenant substitution, expiry of
the URL, a replay, the refusal of a missing or spent key, 413 and 415) and the
sweep are `@profile:embedded`, as for read URLs; the cloud profile's URL is
the provider's, and the nightly `@live` run proves it.
