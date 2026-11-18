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
	stageSeq uint64
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

func (s *Store) contentLength(ctx context.Context, tid core.TenantID, key string) (int64, error) {
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

// ContentType returns the stored content type for key under tid, or "" if unset.
func (s *Store) ContentType(ctx context.Context, tid core.TenantID, key string) (string, error) {
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

func serveContentType(stored, objectType string) string {
	if stored != "" {
		return stored
	}
	if objectType != "" {
		return objectType
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

// Put implements platform.Blob.
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

// Get implements platform.Blob.
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

// Exists implements platform.Blob.
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

// Delete implements platform.Blob.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.checkKey(key); err != nil {
		return err
	}
	tid, err := s.tenantOK(ctx)
	if err != nil {
		return err
	}
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
	_ = s.bucket.Delete(ctx, mp) // meta may be absent for legacy rows
	return nil
}

// SignedURL implements platform.Blob.
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
