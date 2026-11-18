package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"

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

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w, err := s.bucket.NewWriter(wctx, path, &blob.WriterOptions{ContentType: contentType})
	if err != nil {
		return mapError(err)
	}

	h := sha256.New()
	tr := io.TeeReader(r, h)
	_, copyErr := io.Copy(w, tr)
	got := hex.EncodeToString(h.Sum(nil))
	hashOK := got == want

	if copyErr != nil || !hashOK || ctx.Err() != nil {
		cancel()
		_ = w.Close()
		if ctx.Err() != nil {
			return core.NewError(core.CodeCancelled, "write cancelled", nil)
		}
		if !hashOK {
			return core.NewError(core.CodeInvalidArgument, "content does not match content key", nil)
		}
		return core.NewError(core.CodeInvalidArgument, "write source failed", nil)
	}

	mp, err := metaPath(tid, key)
	if err != nil {
		cancel()
		_ = w.Close()
		return err
	}
	if err := s.writeMeta(ctx, mp, contentType); err != nil {
		cancel()
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
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
