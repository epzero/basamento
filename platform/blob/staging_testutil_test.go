package blob

import (
	"context"

	"gocloud.dev/blob"

	"github.com/epzero/basamento/core"
)

func writeStagedObjectForTest(ctx context.Context, s *Store, tid core.TenantID, stageKey string, body []byte, contentType string) error {
	path, err := stageObjectPath(tid, stageKey)
	if err != nil {
		return err
	}
	return s.bucket.WriteAll(ctx, path, body, &blob.WriterOptions{ContentType: contentType})
}

func stagedObjectExistsForTest(ctx context.Context, s *Store, tid core.TenantID, stageKey string) (bool, error) {
	path, err := stageObjectPath(tid, stageKey)
	if err != nil {
		return false, err
	}
	return s.bucket.Exists(ctx, path)
}
