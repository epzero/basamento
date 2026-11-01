# ADR-0004 — Ports with embedded and cloud implementations

**Status:** accepted · **Date:** 2026-11-01

## Decision
Every infrastructure concern is a port in `platform` with an embedded implementation (same process) and a cloud implementation, selected by URL in configuration:

| Port | Embedded | Cloud |
| --- | --- | --- |
| SQL | SQLite (`modernc.org/sqlite`) | Postgres (`pgx`); goose migrations for both |
| Blob | `gocloud.dev/blob` fileblob | s3, gcs, azblob |
| Bus + KV | `nats-server` in-process with JetStream | NATS cluster |
| Secrets (key wrapping) | `gocloud.dev/secrets` localsecrets + Tink | KMS or Vault Transit + Tink |
| Credentials (the vault) | SQL rows, envelope-encrypted through Secrets | the same, or an external store (HashiCorp Vault, a cloud secret manager) |
| Telemetry | OTel spans to SQLite | OTLP |

Wiring uses plain constructors (`wireEmbedded`, `wireCloud`) and koanf configuration. Every port has a conformance suite that runs against both implementations.

**Credentials** are named secrets per tenant (vendor API keys, tokens).
Flows, manifests and state carry only a reference, `cred://<name>`. A value is
written or rotated through the API, resolved inside a step at the moment of
use, and never read back, logged or stored in history. Every use writes an
audit event.

## Consequences
SQL is written once for two dialects and tested on both. Embedded NATS adds a JetStream directory next to the SQLite files; backups cover the whole data directory.
