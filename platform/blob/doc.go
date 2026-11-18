// Package blob is the content-addressed blob store (ADR-0004). Blobs live under
// a tenant prefix in a single bucket opened from a file, S3, GCS or Azure URL.
// The embedded profile keeps data in a local directory and serves proxied read
// and upload URLs; the cloud profile uses the provider's presigned PUT and GET
// URLs (ADR-0013). Clients stage bodies through StartUpload and Commit.
//
// Staging slots are serialized in-process by a per-(tenant, staging key) lock.
// Commit holds that lock for the whole operation. A proxied PUT streams the body
// into the bucket writer without the lock (O(copy-buffer) memory, not O(max)),
// then takes the lock only to re-check the slot and call Close, which publishes
// the staged object. A failed or refused Close aborts the writer and leaves any
// prior staged object in place. The lock does not span cloud replicas: two
// commits on different hosts may return the same content key.
//
// Expired embedded slots are swept lazily: after signature-verified handler or
// store calls, at most once per maximum URL lifetime, with a one-minute retry
// after a failed list. Sweeps list each tenant's stage/ prefix only.
//
// Tenant ids must be one safe path segment until shared validation lands in core
// (EP0-52); see validateTenantSegment.
package blob
