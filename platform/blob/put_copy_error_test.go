package blob

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"gocloud.dev/gcerrors"

	"github.com/epzero/basamento/core"
)

type chunkReader struct {
	b     []byte
	off   int
	chunk int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.off >= len(c.b) {
		return 0, io.EOF
	}
	n := c.chunk
	if n <= 0 {
		n = 1
	}
	if n > len(p) {
		n = len(p)
	}
	if rem := len(c.b) - c.off; n > rem {
		n = rem
	}
	copy(p, c.b[c.off:c.off+n])
	c.off += n
	return n, nil
}

func TestPutWriterFailMidStreamUnavailable(t *testing.T) {
	t.Parallel()
	codes := []gcerrors.ErrorCode{gcerrors.Unknown, gcerrors.Internal, gcerrors.ResourceExhausted}
	body := make([]byte, 256)
	for i := range body {
		body[i] = byte(i)
	}
	key := ContentKeyOf(body)
	for _, code := range codes {
		t.Run(fmt.Sprintf("code-%d", code), func(t *testing.T) {
			t.Parallel()
			st := openStoreFailWriteAt(t, 2, code)
			ctx := core.WithTenant(context.Background(), "acme")
			err := st.Put(ctx, key, &chunkReader{b: body, chunk: 64}, "text/plain")
			if err == nil || core.CodeOf(err) != core.CodeUnavailable {
				t.Fatalf("want unavailable, got %v (code %v)", err, core.CodeOf(err))
			}
			if !core.IsRetryable(err) {
				t.Fatalf("want retryable unavailable, got %v", err)
			}
			if strings.Contains(err.Error(), "content does not match content key") {
				t.Fatalf("writer failure misclassified as hash mismatch: %v", err)
			}
			assertPutLeftNoObjectOrMeta(t, st, ctx, key)
		})
	}
}

func TestPutWriterCloseFailsUnavailableRetryable(t *testing.T) {
	t.Parallel()
	body := []byte("hello")
	key := ContentKeyOf(body)
	st := openStoreFailWriterClose(t, gcerrors.Unknown)
	ctx := core.WithTenant(context.Background(), "acme")
	err := st.Put(ctx, key, strings.NewReader(string(body)), "text/plain")
	if err == nil || core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("want unavailable, got %v", err)
	}
	if !core.IsRetryable(err) {
		t.Fatalf("want retryable, got %v", err)
	}
	assertPutLeftNoObjectOrMeta(t, st, ctx, key)
}

func TestPutReaderFailureStillInvalidArgument(t *testing.T) {
	t.Parallel()
	st := openStoreFailWriteAt(t, 999, gcerrors.Unknown)
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("hello"))
	err := st.Put(ctx, key, &failAfterReader{b: []byte("hello"), limit: 2}, "text/plain")
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
	if !strings.Contains(err.Error(), "write source failed") {
		t.Fatalf("message %q", err.Error())
	}
}

func TestPutHashMismatchInvalidArgument(t *testing.T) {
	t.Parallel()
	st := openStoreFailWriteAt(t, 999, gcerrors.Unknown)
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("hello"))
	err := st.Put(ctx, key, &chunkReader{b: []byte("not-hello-content-bytes"), chunk: 8}, "text/plain")
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
	if !strings.Contains(err.Error(), "content does not match content key") {
		t.Fatalf("message %q", err.Error())
	}
	assertPutLeftNoObjectOrMeta(t, st, ctx, key)
}

func assertPutLeftNoObjectOrMeta(t *testing.T, st *Store, ctx context.Context, key string) {
	t.Helper()
	if ok, _ := st.Exists(ctx, key); ok {
		t.Fatal("object leaked")
	}
	tid, err := st.tenantOK(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mp, err := metaPath(tid, key)
	if err != nil {
		t.Fatal(err)
	}
	if metaOK, _ := st.bucket.Exists(ctx, mp); metaOK {
		t.Fatal("meta leaked")
	}
}

type signalChunkReader struct {
	chunkReader
	signal chan<- struct{}
	fired  bool
}

func (s *signalChunkReader) Read(p []byte) (int, error) {
	if !s.fired && s.signal != nil {
		s.fired = true
		s.signal <- struct{}{}
	}
	return s.chunkReader.Read(p)
}

func TestPutCancelledDuringBackendWrite(t *testing.T) {
	st := openStoreBlockWriteUntilCancel(t, 2)
	ctx, cancel := context.WithCancel(core.WithTenant(context.Background(), "acme"))
	body := make([]byte, 64*1024)
	for i := range body {
		body[i] = byte(i)
	}
	key := ContentKeyOf(body)
	ch := make(chan struct{}, 1)
	go func() {
		<-ch
		cancel()
	}()
	err := st.Put(ctx, key, &signalChunkReader{chunkReader: chunkReader{b: body, chunk: 32}, signal: ch}, "text/plain")
	if err == nil || core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("want cancelled, got %v", err)
	}
	ok, _ := st.Exists(ctx, key)
	if ok {
		t.Fatal("object after cancel")
	}
}
