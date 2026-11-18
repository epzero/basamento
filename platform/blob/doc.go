// Package blob is the content-addressed blob store (ADR-0004). Blobs live under
// a tenant prefix in a single bucket opened from a file, S3, GCS or Azure URL.
// The embedded profile keeps data in a local directory and serves proxied read
// URLs; the cloud profile uses the provider's signed GET URLs.
package blob
