package blob

import (
	"context"
	"io"
	"net/http"
	"time"

	"gocloud.dev/blob"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

// Store is a tenant-scoped content store over one bucket.
type Store struct {
	bucket   bucketBackend
	clock    core.Clock
	maxTTL   time.Duration
	embedded bool
	baseURL  string
	signKey  []byte
	putLocks keyedMu
}

// Close releases the underlying bucket.
func (s *Store) Close() error {
	return s.bucket.Close()
}

func (s *Store) tenantOK(ctx context.Context) (core.TenantID, error) {
	tid, ok := core.TenantFrom(ctx)
	if !ok {
		return "", core.NewError(core.CodeInvalidArgument, "tenant required", nil)
	}
	if err := validateTenantSegment(string(tid)); err != nil {
		return "", err
	}
	return tid, nil
}

func (s *Store) contentLength(ctx context.Context, key string) (int64, error) {
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return 0, err
	}
	path, err := objectPath(tid, key)
	if err != nil {
		return 0, err
	}
	attrs, err := s.bucket.Attributes(ctx, path)
	if err != nil {
		return 0, mapError(err)
	}
	return attrs.Size, nil
}

// ContentType returns the stored content type for key, or "" if unset.
// Tenancy is taken from ctx, never from an argument.
func (s *Store) ContentType(ctx context.Context, key string) (string, error) {
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return "", err
	}
	mp, err := metaPath(tid, key)
	if err != nil {
		return "", err
	}
	r, err := s.bucket.NewReader(ctx, mp, nil)
	if err != nil {
		if gcerr := mapError(err); gcerr != nil && core.CodeOf(gcerr) == core.CodeNotFound {
			return "", nil
		}
		return "", mapError(err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		return "", mapError(err)
	}
	return string(b), nil
}

func serveContentType(stored string) string {
	if stored != "" {
		return stored
	}
	return "application/octet-stream"
}

func (s *Store) hasObject(ctx context.Context, tid core.TenantID, key string) (bool, error) {
	path, err := objectPath(tid, key)
	if err != nil {
		return false, err
	}
	ok, err := s.bucket.Exists(ctx, path)
	if err != nil {
		return false, mapError(err)
	}
	return ok, nil
}

func (s *Store) checkKey(key string) error {
	if !ValidateContentKey(key) {
		return core.NewError(core.CodeInvalidArgument, "malformed content key", nil)
	}
	return nil
}

// Put stores content under a content key. The tenant comes from ctx.
// It returns invalid_argument for a malformed key, for content that does not hash
// to the key, or for a source reader that fails; cancelled when ctx is done;
// unavailable for a backend or writer failure. For an existing object the new
// content is verified against the key and the first content type wins per process.
//
// New keys stream once to the final object path, hashing as they go. On copy
// error, cancellation or hash mismatch the writer context is cancelled before
// Close and nothing is deleted. After a matching hash the sidecar is written,
// then Close commits the object. Two replicas writing the same new key commit
// identical bytes (content-addressed); the in-process keyed lock enforces first
// content type per process. On S3, cancelling a multipart upload may leave
// parts at the provider; operators should use an AbortIncompleteMultipartUpload
// lifecycle rule.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	if err := s.checkKey(key); err != nil {
		return err
	}
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return err
	}
	hex, err := HexOf(key)
	if err != nil {
		return err
	}
	unlock := s.putLocks.lock(string(tid) + "/" + hex)
	defer unlock()
	exists, err := s.hasObject(ctx, tid, key)
	if err != nil {
		return err
	}
	if exists {
		return s.putExisting(ctx, tid, key, r)
	}
	return s.putNew(ctx, tid, key, r, contentType)
}

// Get returns a reader for key. The tenant comes from ctx; a missing object is not_found.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := s.checkKey(key); err != nil {
		return nil, err
	}
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return nil, err
	}
	path, err := objectPath(tid, key)
	if err != nil {
		return nil, err
	}
	r, err := s.bucket.NewReader(ctx, path, nil)
	if err != nil {
		return nil, mapError(err)
	}
	return r, nil
}

// Exists reports whether key is stored for the tenant in ctx.
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	if err := s.checkKey(key); err != nil {
		return false, err
	}
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return false, err
	}
	ok, err := s.hasObject(ctx, tid, key)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// Delete removes key for the tenant in ctx; a missing object is not_found.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.checkKey(key); err != nil {
		return err
	}
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return err
	}
	hex, err := HexOf(key)
	if err != nil {
		return err
	}
	unlock := s.putLocks.lock(string(tid) + "/" + hex)
	defer unlock()
	path, err := objectPath(tid, key)
	if err != nil {
		return err
	}
	if ok, err := s.hasObject(ctx, tid, key); err != nil {
		return err
	} else if !ok {
		return core.NewError(core.CodeNotFound, "blob not found", nil)
	}
	if err := s.bucket.Delete(ctx, path); err != nil {
		return mapError(err)
	}
	mp, err := metaPath(tid, key)
	if err != nil {
		return err
	}
	_ = s.bucket.Delete(ctx, mp)
	return nil
}

// SignedURL returns a proxied or presigned read URL for key (tenant from ctx).
func (s *Store) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := s.checkKey(key); err != nil {
		return "", err
	}
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return "", err
	}
	if ttl <= 0 || ttl > s.maxTTL {
		return "", core.NewError(core.CodeInvalidArgument, "blob URL lifetime out of bounds", nil)
	}
	ok, err := s.hasObject(ctx, tid, key)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", core.NewError(core.CodeNotFound, "blob not found", nil)
	}
	if s.embedded {
		return s.signedProxied(tid, key, ttl)
	}
	path, err := objectPath(tid, key)
	if err != nil {
		return "", err
	}
	u, err := s.bucket.SignedURL(ctx, path, &blob.SignedURLOptions{Method: http.MethodGet, Expiry: ttl})
	if err != nil {
		return "", mapError(err)
	}
	return u, nil
}

var _ platform.Blob = (*Store)(nil)
