package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"

	"gocloud.dev/blob"

	"github.com/epzero/basamento/core"
)

type trackedReader struct {
	inner io.Reader
	err   error
}

func (t *trackedReader) Read(p []byte) (int, error) {
	n, err := t.inner.Read(p)
	if err != nil && err != io.EOF {
		t.err = err
	}
	return n, err
}

func (t *trackedReader) readFailed() bool {
	return t.err != nil
}

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

	trk := &trackedReader{inner: r}
	h := sha256.New()
	tr := io.TeeReader(trk, h)
	_, copyErr := io.Copy(w, tr)
	got := hex.EncodeToString(h.Sum(nil))
	hashOK := got == want

	if copyErr != nil || !hashOK || ctx.Err() != nil {
		cancel()
		_ = w.Close()
		if ctx.Err() != nil {
			return core.NewError(core.CodeCancelled, "write cancelled", nil)
		}
		if copyErr != nil {
			if trk.readFailed() {
				return core.NewError(core.CodeInvalidArgument, "write source failed", nil)
			}
			return mapError(copyErr)
		}
		if !hashOK {
			return core.NewError(core.CodeInvalidArgument, "content does not match content key", nil)
		}
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
