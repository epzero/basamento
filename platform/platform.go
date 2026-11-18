// Package platform declares infrastructure ports. Each port has an embedded
// implementation (OSS / self-host, same process) and a cloud implementation
// (SaaS), selected by URL in configuration. See ADR-0004.
//
// Credentials takes its tenant from the context (core.TenantFrom); Audit
// events do too, and an event without one is deployment-level. See Security
// in docs/architecture/overview.md.
//
//	SQL      sqlite:///data/kit.db            ↔ postgres://…
//	Blob     file:///data/blobs               ↔ s3://… gs://… azblob://…   (gocloud.dev/blob)
//	Bus      nats-embedded:///data/nats       ↔ nats://…                   (NATS JetStream)
//	Secrets  localsecrets://<key>             ↔ awskms://… gcpkms://… hashivault://…  (gocloud.dev/secrets + Tink)
//	Runtime  temporal-embedded:///data/tmp.db ↔ temporal://host:7233  |  local://
//
// Audit has no URL: it is backed by the SQL store configured by SQL, shared
// by every producer.
//
// Bus and KV ignore core.TenantFrom. Subjects and keys carry no tenant; the
// exact string is the isolation boundary. Layers above these ports must bind
// that string to an already authorized run or tenant and must never forward a
// client-chosen subject or key (Purge and Delete included).
//
// A malformed subject is empty, has whitespace, a wildcard token ('*' or '>'),
// or is not one or more dot-separated tokens of letters, digits, '-', and '_'
// with no empty token. A malformed key is empty, has whitespace or a wildcard,
// starts or ends with '.', or is not made of letters, digits, '-', '_', '/',
// '=', and '.'. Every Bus and KV method rejects a malformed subject or key
// with code invalid_argument before touching storage.
//
// Status: contract only (WP-L1-*).
package platform

import (
	"context"
	"io"
	"time"

	"github.com/epzero/basamento/core"
)

// Blob is content storage. Keys are content hashes for immutable assets.
type Blob interface {
	Put(ctx context.Context, key string, r io.Reader, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) // embedded: proxied URL
}

// Message is one retained bus payload. Seq is the bus sequence JetStream
// assigned when the message was published; it increases on that subject but
// need not be contiguous, so a gap is not a missing delivery.
type Message struct {
	Subject string
	Seq     uint64
	Data    []byte
}

// Bus is publish/subscribe with replay by bus sequence, then live delivery on
// the same ordered stream. Embedded and cloud implementations share this
// contract (ADR-0004); the streaming layer replays and purges through it
// (ADR-0005).
type Bus interface {
	// Publish stores data on subject and returns a positive (>= 1) bus sequence,
	// greater than every earlier sequence on that subject (LastSeq reserves zero
	// for none, and Subscribe reserves zero for from the start). An oversize
	// payload or a malformed
	// subject fails with code invalid_argument and nothing is stored. When the
	// server cannot be reached, it fails at once with code unavailable and does
	// not buffer the publish while disconnected.
	Publish(ctx context.Context, subject string, data []byte) (uint64, error)
	// Subscribe delivers every retained message on subject with bus sequence
	// greater than after, then each later publish, in sequence order and each
	// exactly once. After zero means from the start (all retained messages), not
	// live only; for only messages published after the call, pass
	// LastSeq(ctx, subject) as after (a message published between LastSeq and
	// Subscribe is still delivered). Only messages on exactly subject are
	// delivered. If after is positive but below the subject's purge floor (the
	// last purged sequence), Subscribe fails with code not_found and detail
	// purged_through set to that floor; after zero and after at or above the
	// floor are accepted. A slow subscriber holds at most a bounded buffer of
	// undelivered messages; when it fills, the subscription ends after the
	// buffered messages without blocking publishers or other subscribers.
	// Cancellation of ctx ends the subscription; in both cases the receive
	// channel closes and the caller may open a new Subscribe from the last
	// sequence it received. A malformed subject fails with code invalid_argument.
	// When the server cannot be reached, it fails at once with code unavailable.
	Subscribe(ctx context.Context, subject string, after uint64) (<-chan Message, error)
	// LastSeq returns the highest bus sequence ever assigned on subject, or zero
	// if none was ever published, whether or not those messages were later
	// purged. A malformed subject fails with code invalid_argument. When the
	// server cannot be reached, it fails at once with code unavailable.
	LastSeq(ctx context.Context, subject string) (uint64, error)
	// Purge drops every retained message on subject. Later publishes keep
	// receiving greater sequences than any assigned before. The purge floor
	// advances so a replay from below it is refused (see Subscribe). A malformed
	// subject fails with code invalid_argument. When the server cannot be
	// reached, it fails at once with code unavailable.
	Purge(ctx context.Context, subject string) error
}

// KV is a small key-value store with revisions and optional TTLs (locks,
// cursors, presence). Whether an entry has expired is decided solely by the
// injected core.Clock: once clock.Now() is at or past its deadline (the time
// of the put, swap or create plus the ttl), the entry is absent. Any
// server-side TTL is cleanup only and never expires an entry early.
type KV interface {
	// Get returns the latest value and revision for key. A missing or expired
	// entry is reported with code not_found. A malformed key fails with code
	// invalid_argument. When the server cannot be reached, it fails at once with
	// code unavailable.
	Get(ctx context.Context, key string) ([]byte, uint64, error)
	// Put stores value under key with an optional lease, ignoring any current
	// revision, so it must not be used to overwrite or release a contested lease;
	// CompareAndSwap is the call that checks the revision. A ttl of zero means no
	// expiry; a non-zero ttl sets a deadline from the time of the put. It returns
	// a positive (>= 1) revision strictly greater than every earlier revision of
	// that key, including revisions of deleted and expired entries. A malformed
	// key or a negative ttl fails with code invalid_argument. When the server
	// cannot be reached, it fails at once with code unavailable.
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) (uint64, error)
	// CompareAndSwap replaces value when revision is the current revision of an
	// unexpired entry and returns a positive (>= 1) revision strictly greater
	// than every earlier revision of that key, including revisions of deleted
	// and expired entries. Revision zero means create: it stores only when the
	// key has no unexpired entry (missing, deleted, or expired counts as absent)
	// and returns such a revision; an unexpired entry makes it fail with code
	// conflict without changing the entry. A non-zero revision when the key has no
	// unexpired entry fails with code conflict and leaves the key absent. A
	// non-zero revision that is not the current revision of an unexpired entry
	// fails with code conflict and leaves the entry unchanged. Swapping at the
	// revision of an expired entry fails with code conflict. A ttl of zero means
	// no expiry; a non-zero ttl sets a new deadline from the time of the swap.
	// A malformed key or a negative ttl fails with code invalid_argument. When the
	// server cannot be reached, it fails at once with code unavailable.
	CompareAndSwap(ctx context.Context, key string, value []byte, revision uint64, ttl time.Duration) (uint64, error)
	// Delete removes key, ignoring any current revision, so it must not be used to
	// release a contested lease; CompareAndSwap is the call that checks the
	// revision. Deleting a missing, expired, or already absent key succeeds and
	// leaves the key absent. A malformed key fails with code invalid_argument.
	// When the server cannot be reached, it fails at once with code unavailable.
	Delete(ctx context.Context, key string) error
}

// Secrets encrypts small values (data keys, credentials) at rest.
type Secrets interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// Credentials is the vault: named secrets per tenant, such as vendor API keys.
// Callers hold only a reference ("cred://<name>"). The tenant comes from ctx
// (core.TenantFrom) and never from the name.
//
// Every operation fails closed with code invalid_argument, before touching
// storage or returning a value, when core.TenantFrom reports no tenant; none
// falls back to an empty or default tenant. A name is 1 to 128 characters
// from A-Z, a-z, 0-9, ".", "_", "-" and "/", made of "/"-separated segments
// none of which is empty, "." or ".."; so it has no leading or trailing
// "/", no "//" and no ".." segment. Equivalently, with the length limit
// checked separately, a name matches
//
//	^(?:[A-Za-z0-9_-][A-Za-z0-9._-]*|\.(?:[A-Za-z0-9_-]|[A-Za-z0-9._-]{2,}))(?:/(?:[A-Za-z0-9_-][A-Za-z0-9._-]*|\.(?:[A-Za-z0-9_-]|[A-Za-z0-9._-]{2,})))*$
//
// A reference must be "cred://" followed by such a name. Anything else,
// including "/", "..", "?", "#", whitespace and control characters, is
// rejected with code invalid_argument. The caller-supplied name is the only
// part of the store key after the tenant (ADR-0004), and its segments keep
// one tenant out of another's entries.
// Errors and AuditEvents never echo the rejected name or reference: the
// store key it would have formed is a secret (core.Error.Message is not
// redacted) and the audit store is tenant-readable.
type Credentials interface {
	// Put stores or rotates a value under name, for the tenant of ctx.
	Put(ctx context.Context, name string, value []byte) error
	// Resolve returns the value for ref, "cred://<name>". Call it only
	// inside a step, at the moment of use. It validates the tenant and the
	// reference, then looks the value up and decrypts it, then records an
	// AuditEvent with action "credential.resolve" and resource "cred://<name>",
	// outcome "allowed" on success or "failed" on a lookup or decryption error,
	// whose Reason is only a safe category or code, never the value or key
	// material. If the record cannot be written, Resolve returns no value and
	// fails with code unavailable; the value is returned only after the record
	// succeeded.
	Resolve(ctx context.Context, ref string) ([]byte, error)
	// Delete removes name for the tenant of ctx.
	Delete(ctx context.Context, name string) error
	// Names lists what the tenant of ctx has, in any order, never the
	// values. It needs no name, so no grammar applies to its input.
	Names(ctx context.Context) ([]string, error)
}

// AuditEvent is one security-relevant decision: credential use, an
// authorization denial, a worker joining or leaving a pool. It never carries
// a secret value, only the name or reference of what was used, and never a
// raw bearer, pool or join token.
type AuditEvent struct {
	// Time is when the decision was made, from core.Clock.
	Time time.Time
	// Tenant is the tenant the decision was made for. It is the isolation
	// key of the audit store. Record derives it from the context with
	// core.TenantFrom:
	//   - with a tenant in ctx, e.Tenant must be empty or equal to it;
	//     Record rejects a different one with code invalid_argument rather
	//     than overwrite it or trust the event over the context, which would
	//     let a caller write into another tenant's rows.
	//   - without a tenant in ctx, e.Tenant must be empty; Record rejects a
	//     set e.Tenant, so the context stays the only source of the tenant and
	//     no implementation can diverge on it (SQLite or Postgres RLS).
	// Record stores the effective tenant (the context's, or none). An event
	// with none is a deployment-level event.
	Tenant core.TenantID
	// Principal is who acted: a user ID or service token ID, never a raw
	// token secret (pool and join tokens are stored hashed and never
	// logged). Empty when unknown, as for a refused join with an unknown
	// token.
	Principal string
	// Action is what was attempted: namespaced as "<area>.<verb>", such as
	// "credential.resolve", "authz.deny", "worker.join", "worker.leave",
	// "worker.remove", "worker.refuse", "launch.request" or
	// "launch.fail".
	Action string
	// Resource is what it was attempted on, such as a credential reference
	// ("cred://<name>"), a pool id or a worker template id. It never holds
	// a value the caller supplied unchecked: a credential name outside the
	// Credentials grammar is reported as "[invalid]" (see Credentials).
	Resource string
	// Outcome is "allowed", "denied" or "failed".
	Outcome string
	// RunID and Step locate the decision inside a run, empty outside one.
	RunID string
	Step  string
	// Reason explains a denial or failure, empty for an allowed outcome. It
	// is a safe category or error code, never a value or key material.
	Reason string
}

// Audit records security-relevant decisions. There is one append-only store
// per deployment, owned by the credential store (WP-L1-06); authorization
// (WP-L8-02), the worker and launch paths (ADR-0012 §10, §12) and other
// producers write through this port.
//
// Events a tenant made with a tenant in ctx are stored under that tenant and
// read back by that tenant only. Events with no tenant in ctx are
// deployment-level: a refused join with an unknown or revoked pool token
// (REQ-MODEL-029), a join, leave or removal of a shared-pool worker, a
// launch or failed launch of the shared pool, and operator or scaler actions.
// They belong to the deployment, are written with no tenant even when the
// action touches a tenant's pool (a launch token names its pool in
// Resource), and tenant-scoped reads never return them.
type Audit interface {
	// Record appends e for the effective tenant defined on AuditEvent.Tenant,
	// or rejects it with code invalid_argument as described there. A
	// tenant-scoped producer must fail closed when it has no tenant rather
	// than record a tenantless event. It returns an error when the record
	// was not stored; callers on a security path fail closed on that error.
	Record(ctx context.Context, e AuditEvent) error
}
