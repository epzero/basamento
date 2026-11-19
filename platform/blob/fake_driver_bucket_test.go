package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"gocloud.dev/blob"
	"gocloud.dev/blob/driver"
	"gocloud.dev/gcerrors"
)

var errTestNotFound = errors.New("not found")

type codedDriverErr struct {
	code gcerrors.ErrorCode
	msg  string
}

func (e *codedDriverErr) Error() string { return e.msg }

type testBucket struct {
	mu       sync.Mutex
	data     map[string][]byte
	failAt   int
	failCode gcerrors.ErrorCode
	onOp     func(op, key string)
}

func (b *testBucket) fireOp(op, key string) {
	if b.onOp != nil {
		b.onOp(op, key)
	}
}

func newTestBucket(failAt int, code gcerrors.ErrorCode) *testBucket {
	return &testBucket{
		data:     make(map[string][]byte),
		failAt:   failAt,
		failCode: code,
	}
}

func (b *testBucket) ErrorCode(err error) gcerrors.ErrorCode {
	var c *codedDriverErr
	if errors.As(err, &c) {
		return c.code
	}
	switch {
	case errors.Is(err, errTestNotFound):
		return gcerrors.NotFound
	case errors.Is(err, context.Canceled):
		return gcerrors.Canceled
	default:
		return gcerrors.Unknown
	}
}

func (b *testBucket) As(any) bool             { return false }
func (b *testBucket) ErrorAs(error, any) bool { return false }

func (b *testBucket) Attributes(_ context.Context, key string) (*driver.Attributes, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.data[key]
	if !ok {
		return nil, errTestNotFound
	}
	return &driver.Attributes{Size: int64(len(p)), ModTime: time.Unix(0, 0)}, nil
}

func (b *testBucket) ListPaged(context.Context, *driver.ListOptions) (*driver.ListPage, error) {
	return &driver.ListPage{}, nil
}

func (b *testBucket) NewRangeReader(_ context.Context, key string, offset, length int64, _ *driver.ReaderOptions) (driver.Reader, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.data[key]
	if !ok {
		return nil, errTestNotFound
	}
	if offset > int64(len(p)) {
		return nil, errTestNotFound
	}
	slice := p[offset:]
	if length >= 0 && length < int64(len(slice)) {
		slice = slice[:length]
	}
	return &testReader{b: slice}, nil
}

type testReader struct {
	b   []byte
	off int
}

func (r *testReader) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}

func (r *testReader) Close() error { return nil }

func (r *testReader) Attributes() *driver.ReaderAttributes { return &driver.ReaderAttributes{} }

func (r *testReader) As(any) bool { return false }

func (b *testBucket) NewTypedWriter(ctx context.Context, key, contentType string, _ *driver.WriterOptions) (driver.Writer, error) {
	b.fireOp("NewTypedWriter", key)
	w := &testWriter{ctx: ctx, bucket: b, key: key}
	if b.failAt > 0 {
		return &failOnKWriter{ctx: ctx, inner: w, failAt: b.failAt, code: b.failCode}, nil
	}
	return w, nil
}

type testWriter struct {
	ctx      context.Context
	bucket   *testBucket
	key      string
	buf      bytes.Buffer
	done     bool
	closeErr error
}

func (w *testWriter) Write(p []byte) (int, error) {
	if w.done {
		return 0, errors.New("closed")
	}
	return w.buf.Write(p)
}

func (w *testWriter) Close() error {
	if w.done {
		return nil
	}
	w.done = true
	if w.closeErr != nil {
		return w.closeErr
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	w.bucket.mu.Lock()
	defer w.bucket.mu.Unlock()
	w.bucket.data[w.key] = append([]byte(nil), w.buf.Bytes()...)
	return nil
}

type failOnKWriter struct {
	ctx      context.Context
	inner    driver.Writer
	failAt   int
	code     gcerrors.ErrorCode
	block    bool // on the fail write, wait for ctx cancel instead of codedDriverErr
	call     int
	accepted bool
	mu       sync.Mutex
}

func (w *failOnKWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.call++
	if w.call < w.failAt {
		n, err := w.inner.Write(p)
		if n > 0 {
			w.accepted = true
		}
		return n, err
	}
	if w.call == w.failAt {
		if !w.accepted && len(p) > 0 {
			n, err := w.inner.Write(p[:1])
			if n > 0 {
				w.accepted = true
			}
			if err != nil {
				return n, err
			}
		}
		if w.block {
			<-w.ctx.Done()
			return 0, context.Canceled
		}
		return 0, &codedDriverErr{code: w.code, msg: fmt.Sprintf("fail on write %d", w.failAt)}
	}
	return w.inner.Write(p)
}

func (w *failOnKWriter) Close() error {
	return w.inner.Close()
}

func (b *testBucket) Copy(_ context.Context, dst, src string, _ *driver.CopyOptions) error {
	b.fireOp("Copy", dst)
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.data[src]
	if !ok {
		return errTestNotFound
	}
	b.data[dst] = append([]byte(nil), p...)
	return nil
}

func (b *testBucket) Delete(_ context.Context, key string, _ *driver.DeleteOptions) error {
	b.fireOp("Delete", key)
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.data[key]; !ok {
		return errTestNotFound
	}
	delete(b.data, key)
	return nil
}

func (b *testBucket) SignedURL(context.Context, string, *driver.SignedURLOptions) (string, error) {
	return "", errors.New("unimplemented")
}

func (b *testBucket) Close() error { return nil }

func openStoreWithTestBucket(t *testing.T, b *testBucket) *Store {
	t.Helper()
	bkt := blob.NewBucket(b)
	st := &Store{bucket: wrapBucket(bkt)}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func openStoreFailWriteAt(t *testing.T, failAt int, code gcerrors.ErrorCode) *Store {
	t.Helper()
	drv := newTestBucket(failAt, code)
	return openStoreWithTestBucket(t, drv)
}

func openStoreFailWriterClose(t *testing.T, code gcerrors.ErrorCode) *Store {
	t.Helper()
	drv := newTestBucket(0, code)
	bkt := blob.NewBucket(&closeFailBucket{testBucket: drv, closeErr: &codedDriverErr{code: code, msg: "close failed"}})
	st := &Store{bucket: wrapBucket(bkt)}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type closeFailBucket struct {
	*testBucket
	closeErr error
}

func (b *closeFailBucket) NewTypedWriter(ctx context.Context, key, contentType string, _ *driver.WriterOptions) (driver.Writer, error) {
	b.fireOp("NewTypedWriter", key)
	return &testWriter{ctx: ctx, bucket: b.testBucket, key: key, closeErr: b.closeErr}, nil
}

func openStoreBlockWriteUntilCancel(t *testing.T, failAt int) *Store {
	t.Helper()
	drv := newTestBucket(failAt, gcerrors.Unknown)
	bkt := blob.NewBucket(&blockFailBucket{testBucket: drv, failAt: failAt})
	st := &Store{bucket: wrapBucket(bkt)}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type blockFailBucket struct {
	*testBucket
	failAt int
}

func (b *blockFailBucket) NewTypedWriter(ctx context.Context, key, contentType string, _ *driver.WriterOptions) (driver.Writer, error) {
	b.fireOp("NewTypedWriter", key)
	w := &testWriter{ctx: ctx, bucket: b.testBucket, key: key}
	return &failOnKWriter{ctx: ctx, inner: w, failAt: b.failAt, block: true}, nil
}
