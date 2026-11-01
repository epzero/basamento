# L1 — Platform ports

Infrastructure ports with an embedded implementation (same process) and a
cloud implementation, selected by URL (ADR-0004).

### WP-L1-01 — SQL store and migrations
Depends: WP-L0-01.
Owns: `platform/sql`.
SQLite (`modernc.org/sqlite`) and Postgres (`pgx`), one query layer for both dialects, goose migrations from `embed.FS`, tenant-scoped repositories; the conformance suite runs on both.

### WP-L1-02 — Blob store
Depends: WP-L0-01.
Owns: `platform/blob`.
`gocloud.dev/blob` (fileblob ↔ s3/gcs/azblob), content-addressed keys, proxied URLs in the embedded profile.

### WP-L1-03 — Bus and KV
Depends: WP-L0-01.
Owns: `platform/bus`.
Embedded `nats-server` with JetStream ↔ NATS cluster; replay by sequence; KV with leases; in-process fake for unit tests.

### WP-L1-04 — Secrets and payload encryption
Depends: WP-L0-01.
Owns: `platform/secrets`.
`gocloud.dev/secrets` (localsecrets ↔ KMS or Vault Transit) wrapping Tink data keys; the payload encryption the runtimes use.

### WP-L1-05 — Configuration, profiles and `kit serve`
Depends: WP-L1-01, WP-L1-02, WP-L1-03, WP-L1-04.
Owns: `cmd/kit`, `internal/config`.
koanf; URL-selected implementations; plain-constructor wiring for `embedded` and `cloud`. Enables the system smoke gate (G6).

### WP-L1-06 — Credential store
Depends: WP-L1-01, WP-L1-04.
Owns: `platform/credentials`.
The vault (ADR-0004): named secrets per tenant, envelope-encrypted in SQL, `cred://` references resolved inside steps, write-only API, audit on use; external stores behind the same port.

### WP-L1-07 — Telemetry
Depends: WP-L1-01.
Owns: `platform/telemetry`.
`slog` and OpenTelemetry traces and metrics behind one setup; OTLP export in the cloud profile, spans kept in SQLite for the run inspector in the embedded one.
