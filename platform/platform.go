// Package platform declares infrastructure ports. Each port has an embedded
// implementation (OSS / self-host, same process) and a cloud implementation
// (SaaS), selected by URL in configuration. See ADR-0004.
//
//	SQL      sqlite:///data/kit.db            ↔ postgres://…
//	Blob     file:///data/blobs               ↔ s3://… gs://… azblob://…   (gocloud.dev/blob)
//	Bus      nats-embedded:///data/nats       ↔ nats://…                   (NATS JetStream)
//	Secrets  localsecrets://<key>             ↔ awskms://… gcpkms://… hashivault://…  (gocloud.dev/secrets + Tink)
//	Runtime  temporal-embedded:///data/tmp.db ↔ temporal://host:7233  |  local://
//
// Audit has no URL: it is the SQL store behind SQL, shared by every producer.
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

// Message is a bus message.
type Message struct {
	Subject string
	Seq     uint64
	Data    []byte
}

// Bus is publish/subscribe with bounded replay by sequence.
type Bus interface {
	Publish(ctx context.Context, subject string, data []byte) (uint64, error)
	// Subscribe delivers messages on subject with sequence > after (0 = live only).
	Subscribe(ctx context.Context, subject string, after uint64) (<-chan Message, error)
}

// KV is a small key-value store with leases (locks, cursors, presence).
type KV interface {
	Get(ctx context.Context, key string) ([]byte, uint64, error)
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) (uint64, error)
	CompareAndSwap(ctx context.Context, key string, value []byte, revision uint64) (uint64, error)
	Delete(ctx context.Context, key string) error
}

// Secrets encrypts small values (data keys, credentials) at rest.
type Secrets interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// Credentials is the vault: named secrets per tenant, such as vendor API keys.
// Callers hold only a reference ("cred://<name>"). The tenant comes from ctx
// (core.TenantFrom).
type Credentials interface {
	// Put stores or rotates a value. There is no way to read it back.
	Put(ctx context.Context, name string, value []byte) error
	// Resolve returns the value for ref. Call it only inside a step, at the
	// moment of use. Every well-formed reference is recorded through Audit
	// before the value is returned; if the record cannot be written, Resolve
	// returns no value and fails with code unavailable.
	Resolve(ctx context.Context, ref string) ([]byte, error)
	Delete(ctx context.Context, name string) error
	// Names lists what exists, never the values.
	Names(ctx context.Context) ([]string, error)
}

// AuditEvent is one security-relevant decision: credential use, an
// authorization denial, a worker join. It never carries a secret value, only
// the name or reference of what was used.
type AuditEvent struct {
	// Time is when the decision was made, from core.Clock.
	Time time.Time
	// Tenant is the tenant the decision was made for. It is also carried by
	// the context; an implementation resolves the tenant with core.TenantFrom.
	Tenant core.TenantID
	// Principal is who acted (a user or a service token), empty when unknown.
	Principal string
	// Action is what was attempted, such as "credential.resolve".
	Action string
	// Resource is what it was attempted on, such as the credential name.
	Resource string
	// Outcome is "allowed", "denied" or "failed".
	Outcome string
	// RunID and Step locate the decision inside a run, empty outside one.
	RunID string
	Step  string
	// Reason explains a denial or failure, empty for an allowed outcome.
	Reason string
}

// Audit records security-relevant decisions. There is one append-only store
// per deployment, owned by the credential store (WP-L1-06); authorization
// (WP-L8-02) and other producers write through this port.
type Audit interface {
	// Record appends e. It returns an error when the record was not stored;
	// callers on a security path fail closed on that error.
	Record(ctx context.Context, e AuditEvent) error
}
