// Package blob is the content-addressed blob store (ADR-0004). Blobs live under
// a tenant prefix in a single bucket opened from a file, S3, GCS or Azure URL.
// The embedded profile keeps data in a local directory and serves proxied read
// and upload URLs; the cloud profile uses the provider's presigned PUT and GET
// URLs (ADR-0013). Clients stage bodies through StartUpload and Commit.
//
// Tenant ids must be one safe path segment until shared validation lands in core
// (EP0-52); see validateTenantSegment.
package blob
