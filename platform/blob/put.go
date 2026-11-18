package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync/atomic"

	"gocloud.dev/blob"

	"github.com/epzero/basamento/core"
)

func (s *Store) putExisting(ctx context.Context, tid core.TenantID, key string, r io.Reader) error {
	want, err := HexOf(key)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		if ctx.Err() != nil {
			return core.NewError(core.CodeCancelled, "write cancelled", nil)
		}
		return core.NewError(core.CodeInvalidArgument, "write source failed", nil)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return core.NewError(core.CodeInvalidArgument, "content does not match content key", nil)
	}
	return nil
}

func (s *Store) putNew(ctx context.Context, tid core.TenantID, key string, r io.Reader, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	want, err := HexOf(key)
	if err != nil {
		return err
	}
	path, err := objectPath(tid, key)
	if err != nil {
		return err
	}
	seq := atomic.AddUint64(&s.stageSeq, 1)
	stage := fmt.Sprintf("%s/.part/%s/%d", tid, want, seq)
	_ = s.bucket.Delete(ctx, stage)
	defer func() { _ = s.bucket.Delete(ctx, stage) }()

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, err := s.bucket.NewWriter(wctx, stage, &blob.WriterOptions{})
	if err != nil {
		return mapError(err)
	}
	h := sha256.New()
	tr := io.TeeReader(r, h)
	if _, err := io.Copy(w, tr); err != nil {
		cancel()
		_ = w.Close()
		if ctx.Err() != nil {
			return core.NewError(core.CodeCancelled, "write cancelled", nil)
		}
		return core.NewError(core.CodeInvalidArgument, "write source failed", nil)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		cancel()
		_ = w.Close()
		return core.NewError(core.CodeInvalidArgument, "content does not match content key", nil)
	}
	if err := w.Close(); err != nil {
		return mapError(err)
	}
	rd, err := s.bucket.NewReader(ctx, stage, nil)
	if err != nil {
		return mapError(err)
	}
	defer func() { _ = rd.Close() }()

	// Sidecar first, object last: the object existing is the commit point. A crash
	// may leave an orphan .meta; the next Put overwrites it before committing a
	// new object (hasObject only checks the object path).
	mp, err := metaPath(tid, key)
	if err != nil {
		return err
	}
	if err := s.writeMeta(ctx, mp, contentType); err != nil {
		return err
	}
	ww, err := s.bucket.NewWriter(ctx, path, &blob.WriterOptions{ContentType: contentType})
	if err != nil {
		_ = s.bucket.Delete(ctx, mp)
		return mapError(err)
	}
	if _, err := io.Copy(ww, rd); err != nil {
		_ = ww.Close()
		cancel()
		_ = s.bucket.Delete(ctx, path)
		if ctx.Err() != nil {
			return core.NewError(core.CodeCancelled, "write cancelled", nil)
		}
		return core.NewError(core.CodeInvalidArgument, "write source failed", nil)
	}
	if err := ww.Close(); err != nil {
		_ = s.bucket.Delete(ctx, path)
		return mapError(err)
	}
	return nil
}

func (s *Store) writeMeta(ctx context.Context, metaKey, contentType string) error {
	if err := s.bucket.WriteAll(ctx, metaKey, []byte(contentType), nil); err != nil {
		return mapError(err)
	}
	return nil
}
