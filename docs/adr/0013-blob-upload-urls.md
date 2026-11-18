# ADR-0013 — Blob uploads go through a staged upload URL and a commit

**Status:** proposed · **Date:** 2026-11-18

Proposed at the designer's request while the architect was unavailable. The
owner chose the direction on 2026-11-18 (EP0-22). The architect accepts it,
or amends it, before it merges.

## Context

ADR-0012 and [`../architecture/browser-runner.md`](../architecture/browser-runner.md)
say that output files and artifacts reach blob storage "through scoped URLs":
a worker's `Finish` and a browser session's artifacts are announced only
after the files were uploaded. But `platform.Blob` (ADR-0004) has only a
read-side URL:

```go
SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
```

Nothing can upload. Three things make this more than a missing method:

- **The bodies are large.** GPU renders, video and browser recordings are
  gigabytes, and they are produced on machines the platform does not host
  (a worker behind NAT, a browser runner in its own repository).
- **A client must not name a content key.** The platform's own rule is that
  an immutable asset is stored under its content hash (REQ-BLOB-001,
  REQ-BLOB-002). A client that chose `sha256:<hex>` itself could claim a key
  whose content it does not have, overwrite a name it guessed, or learn
  through `Exists` what another tenant holds (REQ-BLOB-010).
- **No work package owned upload URLs.** WP-L1-02 built the store and
  REQ-BLOB-013…017 its read URLs; WP-L3-07 (workers) and WP-L9-01 (browser
  runner) both assume uploads work.

So two gaps meet: the port has no upload, and the roadmap has no owner for
one. This ADR closes both.

## Decision

`platform.Blob` gains two methods, additively. The rest of the port is
unchanged, as are the content keys and the read URLs of WP-L1-02.

### The port

```go
// Blob is content storage. Keys are content hashes for immutable assets.
// Uploads are staged: StartUpload hands out a URL that writes to a fresh
// staging key of the calling tenant, and Commit turns the staged object into
// a content-addressed blob. A client never names a content key.
type Blob interface {
	Put(ctx context.Context, key string, r io.Reader, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)

	StartUpload(ctx context.Context, o UploadOptions) (Upload, error)
	Commit(ctx context.Context, stagingKey string) (key string, err error)
}

// UploadOptions are what a client is allowed to do with an upload URL.
// Every field is a bound, not a preference: the staged object must meet all
// of them, or Commit refuses it.
type UploadOptions struct {
	// MaxSize is the largest body the URL accepts, in bytes. It must be
	// positive.
	MaxSize int64
	// ContentType is the media type the staged object must carry, and the
	// one the blob is stored and served with. It is a plain type/subtype,
	// never a list or a wildcard.
	ContentType string
	// TTL is how long the URL and its staging key live. It must be positive
	// and at most the maximum upload URL lifetime (7 days, as for read URLs,
	// REQ-BLOB-017).
	TTL time.Duration
}

// Upload is one staging slot: where to upload, and which key to commit.
type Upload struct {
	// URL is where the client PUTs the body, with Content-Length at most
	// MaxSize and Content-Type exactly ContentType. It needs no other
	// credentials. Cloud: the provider's presigned PUT. Embedded: a proxied
	// URL served by kit, signed like a read URL (see below).
	URL string
	// StagingKey names this upload in Commit, and in this tenant only. It is
	// opaque, single use, and expires with the TTL.
	StagingKey string
	// Expires is when the URL and the staging key stop working.
	Expires time.Time
}
```

- **`StartUpload(ctx, o)`** takes the tenant from `core.TenantFrom(ctx)`.
  Without a tenant it fails with `invalid_argument` and touches nothing. It
  validates `o` (positive `MaxSize`, a well-formed `ContentType`, `0 < TTL ≤
  max`) with `invalid_argument`, then reserves a **fresh staging key** —
  random, never derived from content — bound to that tenant, `o` and now +
  `TTL`, and returns the URL that writes to it. It never looks at a content
  key.
- **`Commit(ctx, stagingKey)`** verifies, all under the tenant of `ctx` and
  none with cross-tenant fallbacks:
  1. the staging key is well formed, belongs to this tenant and has not
     expired or been committed before, else `not_found`;
  2. the staged object exists and its `Content-Type` is the one bound at
     `StartUpload`, else `not_found` for a missing object and
     `invalid_argument` for the wrong type;
  3. it streams the staged object, computes its SHA-256 and **enforces
     `MaxSize` while streaming** (an object that cannot be fetched within the
     limit, or whose length exceeds it, is `invalid_argument`; the size is
     enforced again at `StartUpload` and by the URL, but never trusted);
  4. it moves the object to the tenant's content key `sha256:<hex>` — the
     write of WP-L1-02, which keeps an existing blob's content type
     (REQ-BLOB-005) — and deletes the staging object;
  5. it returns that key.

  A commit that fails at any step leaves nothing readable: the staging key is
  spent, the staged object is deleted once it has failed its checks, and the
  content key is never written unless the hash matched inside the size
  limit. A `Commit` repeated for a staging key that already succeeded is
  `not_found` too; the caller was told the key in the first answer.
- **Staging keys expire.** Uncommitted staged objects are deleted when their
  TTL passes: the embedded store sweeps expired keys on open and at most
  once per TTL interval; the cloud store sets the bucket's lifecycle rule to
  expire the staging prefix after one week, and `StartUpload` also records
  the expiry so `Commit` refuses late keys even before the sweep runs.

### Embedded and cloud

| Profile | `StartUpload`'s URL | Expiry enforced by |
| --- | --- | --- |
| Embedded (`kit`) | A proxied URL `<base>/blob/upload/<tenant>/<staging id>?exp=<unix>&sig=<HMAC-SHA256 over tenant, staging id, expiry, max size and content type>` served by the upload handler `platform/blob` exports and `kit serve` mounts, next to the read handler. It checks signature, expiry and tenant exactly as REQ-BLOB-013…017, enforces `Content-Length` and `Content-Type`, and writes to the staging key. Status: 403 for a bad signature or expiry, 413 over the size, 415 for the wrong type, 404 for an unknown key, 201 on success. | the handler, and `Commit` |
| Cloud | The provider's presigned PUT (`bucket.SignedURL` with `Method: PUT`, `ContentType` and a signed `Content-Length` range), to the staging prefix. | the provider, the lifecycle rule and `Commit` |

Both profiles pass the same conformance scenarios; the expiry and tampering
scenarios are `@profile:embedded`, as for read URLs.

### Errors

| Situation | Code |
| --- | --- |
| no tenant in `ctx` | `invalid_argument` |
| bad `UploadOptions`, wrong content type, size over `MaxSize` | `invalid_argument` |
| unknown, other tenant's, expired or already committed staging key | `not_found` |
| the store or provider fails | `unavailable` |

There is no `conflict` and no hash-mismatch code: a client cannot name a
hash, so the only hash `Commit` can find is a wrong one it did not ask for,
which is a bad upload like a size overrun.

### Who uses it

- **Workers (WP-L3-07):** before `Finish`, the worker calls `StartUpload`
  with the file's size, type and a lifetime inside its lease, PUTs the file,
  then `Commit`, and reports the returned content key with the result. The
  URLs are scoped to the one invocation (ADR-0012 §10).
- **Browser runner (WP-L9-01):** the same for recordings and artifacts.
- **The platform itself** keeps using `Put` for what it holds in hand.

## Alternatives considered

- **Each consumer implements its own upload.** The worker and the browser
  runner each get a presigned URL from the bucket's own SDK and compute the
  hash themselves. Rejected: it puts the bucket's credentials and the
  content-key rules outside the port, and each consumer would re-invent
  tenancy, size limits, expiry and hashing — three ways for a tenant to write
  into another's keys, and no conformance suite that catches it.
- **Upload through an API endpoint.** The worker or runner POSTs the file to
  `kit`, which streams it into `Blob.Put`. Rejected: the platform's server
  would carry every GPU render and browser video through its own memory,
  CPU and ingress, doubling bandwidth and capping the size by request
  limits, on the machines the platform already wants to keep out of the
  path. It also makes a long upload fail when one platform replica restarts.
- **A signed URL straight to a content key.** The client computes the hash
  and asks for a URL to `sha256:<hex>`. Rejected: the client chooses a
  content key, which REQ-BLOB-002 and the "a client never names a content
  key" rule forbid, and it lets a tenant probe the key space or claim a key
  it cannot produce.
- **`Put` over a signed URL for arbitrary keys (a generic write URL).**
  Rejected for the same reason, plus overwrite semantics: `Put` is
  idempotent for the same content, a raw write URL is not.
- **A hash supplied by the client at `Commit`.** `Commit(ctx, stagingKey,
  wantHash)` and `invalid_argument` when it differs. Rejected for now: the
  size, type and tenant checks already bound what may land under a key, and
  the check would only serve clients that want to catch a corrupt transfer;
  a checksum header on the PUT does that better. It can be added additively.

## Consequences

- Workers and the browser runner can upload without credentials, into their
  own tenant only, and only the platform names the content key. The port
  stays implemented by two stores, with one conformance suite.
- `platform/blob` owns two more handlers' worth of behavior in the embedded
  profile: a signed upload route and a sweep of expired staging keys; the
  cloud profile needs a lifecycle rule on the staging prefix when the bucket
  is configured.
- A failed or abandoned upload costs a staged object until its TTL, not a
  readable blob. A commit is at least one full read of the staged object to
  hash it, twice if the store moves by copy and delete.
- **Work packages.** New **WP-L1-08 — Blob upload URLs** (`docs/roadmap/L1-platform.md`)
  owns the upload part of `platform/blob` and this port change; it depends on
  WP-L1-02. WP-L3-07 and WP-L9-01 depend on WP-L1-08.
- **Specs.** `specs/blob` gains requirements (the upload URL, its bounds, the
  tenant binding, single-use staging keys, `Commit`, expiry, and no readable
  content from a failed commit) and scenarios, in the pull request of
  WP-L1-08, not this one.
- **Docs.** `specs/blob/design.md` says the port's method set "stays as it
  is"; that line is amended with a pointer here, as are ADR-0012 and
  `docs/architecture/browser-runner.md` where they say "scoped URLs".
- **Not decided here:** whether the platform itself accepts multipart
  uploads of objects over the provider's single-PUT limit (S3's 5 GiB); the
  first version sets `MaxSize` accordingly and a caller with a larger file
  splits it or uses `Put`. Nor how a client resumes an interrupted upload.
