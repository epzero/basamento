package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	stage := fmt.Sprintf("%s/.part/%s", tid, want)
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
	// Copy staged bytes to the final key; fileblob has no server-side copy, so
	// reopen the staged object.
	rd, err := s.bucket.NewReader(ctx, stage, nil)
	if err != nil {
		return mapError(err)
	}
	defer func() { _ = rd.Close() }()
	ww, err := s.bucket.NewWriter(ctx, path, &blob.WriterOptions{ContentType: contentType})
	if err != nil {
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
	mp, err := metaPath(tid, key)
	if err != nil {
		return err
	}
	mw, err := s.bucket.NewWriter(ctx, mp, nil)
	if err != nil {
		return mapError(err)
	}
	if _, err := mw.Write([]byte(contentType)); err != nil {
		_ = mw.Close()
		return mapError(err)
	}
	if err := mw.Close(); err != nil {
		return mapError(err)
	}
	return nil
}
