package blob

import (
	"context"
	"io"

	"github.com/epzero/basamento/core"
)

// StagedObjectExists reports whether a staging object exists for tenant tid.
func (s *Store) StagedObjectExists(ctx context.Context, tid core.TenantID, stageKey string) (bool, error) {
	path, err := stageObjectPath(tid, stageKey)
	if err != nil {
		return false, err
	}
	return s.bucket.Exists(ctx, path)
}

// ReadStagedObject returns the staged body and served content type.
func (s *Store) ReadStagedObject(ctx context.Context, tid core.TenantID, stageKey string) ([]byte, string, error) {
	path, err := stageObjectPath(tid, stageKey)
	if err != nil {
		return nil, "", err
	}
	r, err := s.bucket.NewReader(ctx, path, nil)
	if err != nil {
		return nil, "", mapError(err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, "", mapError(err)
	}
	attrs, err := s.bucket.Attributes(ctx, path)
	if err != nil {
		return nil, "", mapError(err)
	}
	return b, serveContentType("", attrs.ContentType), nil
}
