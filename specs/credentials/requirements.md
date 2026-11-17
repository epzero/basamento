# Credentials: the vault

Status: **Planned** (WP-L1-06, `platform/credentials`). Design: ADR-0004,
[`design.md`](design.md). Constitution articles 7 and 8.

The credential store is a tenant's vault: named secrets such as vendor API
keys, kept only encrypted in its backing store (the SQL store in the embedded
profile, an external secret store in the cloud one), written and rotated
through a write-only API and resolved by a `cred://<name>` reference at the
moment of use. Every resolution is audited. Key wrapping and payload
encryption are WP-L1-04's (`specs/secrets/`); authentication and
authorization are WP-L8's; this capability is the store itself.

## Values

### REQ-CRED-001 — Round trip
When a credential value is put under a name for a tenant, the credential store shall return that value when the reference to that name is resolved for that tenant.

### REQ-CRED-002 — Replacement
When a credential value is put under a name a tenant already stores, the credential store shall replace the stored value.

### REQ-CRED-003 — Deletion
When a credential is deleted for a tenant, the credential store shall remove the stored credential.

### REQ-CRED-004 — Deleting a credential that is not stored
If a deletion names a credential the tenant does not store, then the credential store shall fail the deletion with code `not_found`.

### REQ-CRED-005 — Tenant required
If a credential store call carries no tenant in its context, then the credential store shall reject the call with code `invalid_argument`.

### REQ-CRED-006 — Valid credential names
The credential store shall accept a credential name of 1 to 128 characters from `A`–`Z`, `a`–`z`, `0`–`9`, `.`, `_`, `-` and `/`.

### REQ-CRED-007 — Invalid credential names
If a credential name is empty, longer than 128 characters, or contains a character other than `A`–`Z`, `a`–`z`, `0`–`9`, `.`, `_`, `-` and `/`, then the credential store shall reject the call with code `invalid_argument`.

### REQ-CRED-008 — Reference form
The credential store shall resolve only a reference of the form `cred://` followed by a valid credential name.

### REQ-CRED-009 — Malformed references
If a reference is not `cred://` followed by a valid credential name, then the credential store shall reject the resolution with code `invalid_argument`.

### REQ-CRED-010 — Missing credential
If a reference names a credential that no tenant stores, then the credential store shall fail the resolution with code `not_found`.

### REQ-CRED-011 — Another tenant's credential
If a reference names a credential that only another tenant stores, then the credential store shall fail the resolution with code `not_found`.

### REQ-CRED-012 — Listing is scoped to the tenant
When a caller lists credentials, the credential store shall return only credentials stored for the calling tenant.

### REQ-CRED-013 — One name per tenant
When the same credential name is put for two tenants, the credential store shall keep each tenant's value under that name.

### REQ-CRED-014 — Write-only
The credential store shall expose a credential value only as the return of a resolution.

### REQ-CRED-015 — Listing metadata
When a caller lists credentials, the credential store shall return each credential's name, creation time and last-updated time.

### REQ-CRED-016 — Listing order
When a caller lists credentials, the credential store shall return them ordered by the bytes of their names.

## Audit

### REQ-CRED-017 — Audit of a use
When a credential reference is resolved, the credential store shall record an audit event carrying the principal, the credential, the run, the step and the outcome.

### REQ-CRED-018 — Audit of a failed use
If a resolution fails, then the credential store shall record an audit event carrying the principal, the requested credential, the run, the step and the failure.

### REQ-CRED-019 — No value in audit, errors or logs
The credential store shall write a credential value into no audit event, error or log record.

### REQ-CRED-020 — Audit failure fails the use
If the audit event of a resolution cannot be recorded, then the credential store shall fail the resolution with code `internal` and return no value.

## Storage

### REQ-CRED-021 — Encrypted at rest
When a credential value is put, the credential store shall store it only as the ciphertext the Secrets port returns.

### REQ-CRED-022 — Surviving a restart
When the credential store reopens on the same stored credentials and the same master key, the credential store shall resolve credentials stored before the restart.

### REQ-CRED-023 — Undecryptable stored credential
If the Secrets port rejects a stored credential's ciphertext, then the credential store shall fail the resolution with code `internal` and return no value.

### REQ-CRED-024 — Rotation between uses
When a credential value is replaced while a run is in flight, the credential store shall resolve the new value for that run's later uses.

### REQ-CRED-025 — Rotation during a resolution
When a credential value is replaced while a resolution is in flight, the credential store shall complete that resolution with the value it read before the replacement.

### REQ-CRED-026 — External store
Where the credential store is configured with an external store, the credential store shall keep every stored credential only in that external store.
