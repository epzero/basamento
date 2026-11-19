package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gocloud.dev/blob/fileblob"

	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/internal/bdd/s3sim"
)

type failAfterReader struct {
	b     []byte
	off   int
	limit int
}

func (f *failAfterReader) Read(p []byte) (int, error) {
	if f.off >= f.limit {
		return 0, errors.New("read failed")
	}
	n := copy(p, f.b[f.off:])
	f.off += n
	if f.off >= f.limit {
		return n, errors.New("read failed")
	}
	return n, nil
}

func countBucketFiles(dir string) (int, error) {
	n := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		n++
		return nil
	})
	return n, err
}

func assertBucketEmpty(t *testing.T, dir string) {
	t.Helper()
	n, err := countBucketFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("bucket listing want empty, got %d files under %s", n, dir)
	}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := filepath.Base(path)
		if strings.Contains(path, ".part") || strings.HasSuffix(base, ".meta") || strings.Contains(base, ".tmp") {
			t.Fatalf("leftover artifact: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPutFailMidStreamNoObjectFile(t *testing.T) {
	t.Parallel()
	st, _, dir := openFileStore(t)
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("hello world"))
	content := []byte("hello world")
	err := st.Put(ctx, key, &failAfterReader{b: content, limit: 3}, "text/plain")
	if err == nil {
		t.Fatal("want error")
	}
	ok, err := st.Exists(ctx, key)
	if err != nil || ok {
		t.Fatalf("exists ok=%v err=%v", ok, err)
	}
	assertBucketEmpty(t, dir)
	if err := st.Put(ctx, key, bytes.NewReader(content), "text/plain"); err != nil {
		t.Fatal(err)
	}
	rc, err := st.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("body %q err=%v", got, err)
	}
}

func TestPutHashMismatchNoObjectFile(t *testing.T) {
	t.Parallel()
	st, _, dir := openFileStore(t)
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("hello world"))
	err := st.Put(ctx, key, strings.NewReader("wrong body bytes!!"), "text/plain")
	if err == nil {
		t.Fatal("want mismatch error")
	}
	ok, _ := st.Exists(ctx, key)
	if ok {
		t.Fatal("object should not exist")
	}
	assertBucketEmpty(t, dir)
	if err := st.Put(ctx, key, strings.NewReader("hello world"), "text/plain"); err != nil {
		t.Fatal(err)
	}
}

func TestPutFailMidStreamNoObjectS3(t *testing.T) {
	t.Parallel()
	srv := s3sim.Start()
	defer srv.Close()
	_ = os.Setenv("AWS_ACCESS_KEY_ID", "TEST")
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "TEST")
	st, _, err := Open(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("payload"))
	body := []byte("payload-content")
	err = st.Put(ctx, key, &failAfterReader{b: body, limit: 4}, "text/plain")
	if err == nil {
		t.Fatal("want error")
	}
	ok, _ := st.Exists(ctx, key)
	if ok {
		t.Fatal("object leaked on s3")
	}
}

func TestPutCancelMidStreamEmptyFile(t *testing.T) {
	t.Parallel()
	st, _, dir := openFileStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = core.WithTenant(ctx, "acme")
	key := ContentKeyOf([]byte(strings.Repeat("x", 1<<20)))
	done := make(chan error, 1)
	go func() {
		done <- st.Put(ctx, key, &cancelAfterBytes{ctx: ctx, b: []byte(strings.Repeat("x", 1<<20)), cancelAt: 1 << 10, cancel: cancel}, "text/plain")
	}()
	if err := <-done; err == nil || core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("want cancelled, got %v", err)
	}
	ok, _ := st.Exists(ctx, key)
	if ok {
		t.Fatal("object after cancel")
	}
	assertBucketEmpty(t, dir)
}

func TestPutCancelMidStreamEmptyS3(t *testing.T) {
	t.Parallel()
	srv := s3sim.Start()
	defer srv.Close()
	_ = os.Setenv("AWS_ACCESS_KEY_ID", "TEST")
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "TEST")
	st, _, err := Open(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	ctx = core.WithTenant(ctx, "acme")
	key := ContentKeyOf([]byte(strings.Repeat("y", 1<<18)))
	done := make(chan error, 1)
	go func() {
		done <- st.Put(ctx, key, &cancelAfterBytes{ctx: ctx, b: []byte(strings.Repeat("y", 1<<18)), cancelAt: 1 << 12, cancel: cancel}, "text/plain")
	}()
	if err := <-done; err == nil || core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("want cancelled, got %v", err)
	}
	ok, _ := st.Exists(ctx, key)
	if ok {
		t.Fatal("object after cancel on s3")
	}
}

type cancelAfterBytes struct {
	ctx      context.Context
	b        []byte
	off      int
	cancelAt int
	cancel   context.CancelFunc
}

func (c *cancelAfterBytes) Read(p []byte) (int, error) {
	if c.off >= c.cancelAt {
		c.cancel()
		return 0, context.Canceled
	}
	n := copy(p, c.b[c.off:])
	c.off += n
	return n, c.ctx.Err()
}

func openTwoFileStores(t *testing.T) (s1, s2 *Store, dir string) {
	t.Helper()
	dir = t.TempDir()
	b, err := fileblob.OpenBucket(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	clk := coretest.NewFakeClock(time.Unix(1700000000, 0))
	s1 = &Store{bucket: b, clock: clk}
	s2 = &Store{bucket: b, clock: clk}
	return s1, s2, dir
}

func TestPutFailureDoesNotEraseCommittedPeer(t *testing.T) {
	t.Parallel()
	s1, s2, dir := openTwoFileStores(t)
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("committed"))
	if err := s1.Put(ctx, key, strings.NewReader("committed"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	err := s2.Put(ctx, key, &failAfterReader{b: []byte("committed"), limit: 4}, "text/plain")
	if err == nil {
		t.Fatal("want fail from store2")
	}
	rc, err := s1.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	if string(got) != "committed" {
		t.Fatalf("peer corrupted: %q", got)
	}
	ct, err := s1.ContentType(ctx, key)
	if err != nil || ct != "text/plain" {
		t.Fatalf("ctype %q err=%v", ct, err)
	}
	_ = dir
	err = s2.Put(ctx, key, strings.NewReader("bad-content-mismatch!!"), "text/html")
	if err == nil {
		t.Fatal("want mismatch")
	}
	rc2, err := s1.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc2.Close() }()
	got2, _ := io.ReadAll(rc2)
	if string(got2) != "committed" {
		t.Fatalf("after mismatch peer corrupted")
	}
}

func TestPutConcurrentTwoStoresSameKey(t *testing.T) {
	t.Parallel()
	s1, s2, dir := openTwoFileStores(t)
	ctx := core.WithTenant(context.Background(), "acme")
	body := []byte("shared-race-body")
	key := ContentKeyOf(body)
	var wg sync.WaitGroup
	wg.Add(2)
	for _, st := range []*Store{s1, s2} {
		st := st
		go func() {
			defer wg.Done()
			_ = st.Put(ctx, key, bytes.NewReader(body), "text/plain")
		}()
	}
	wg.Wait()
	ok, err := s1.Exists(ctx, key)
	if err != nil || !ok {
		t.Fatalf("exists: %v %v", ok, err)
	}
	rc, err := s1.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, body) {
		t.Fatalf("body %q", got)
	}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := filepath.Base(path)
		if strings.Contains(base, ".tmp") || strings.Contains(path, ".part") {
			t.Fatalf("leftover: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestKeyedMuEmptyAfterPuts(t *testing.T) {
	t.Parallel()
	st, _, _ := openFileStore(t)
	ctx := core.WithTenant(context.Background(), "acme")
	const workers = 24
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			switch i % 4 {
			case 0:
				key := ContentKeyOf([]byte{byte(i)})
				_ = st.Put(ctx, key, bytes.NewReader([]byte{byte(i)}), "text/plain")
			case 1:
				key := ContentKeyOf([]byte("same"))
				_ = st.Put(ctx, key, bytes.NewReader([]byte("same")), "text/plain")
			case 2:
				key := ContentKeyOf([]byte("x"))
				_ = st.Put(ctx, key, strings.NewReader("not-x-content"), "text/plain")
			case 3:
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				key := ContentKeyOf([]byte("z"))
				_ = st.Put(cctx, key, strings.NewReader("z"), "text/plain")
			}
		}()
	}
	wg.Wait()
	if st.putLocks.activeKeys() != 0 {
		t.Fatalf("putLocks not empty: %d", st.putLocks.activeKeys())
	}
}
