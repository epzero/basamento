package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gocloud.dev/blob"
	"gocloud.dev/blob/fileblob"
	"gocloud.dev/gcerrors"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
)

type pauseReader struct {
	started chan struct{}
	release chan struct{}
	data    []byte
	off     int
}

func (p *pauseReader) Read(b []byte) (int, error) {
	if p.off == 0 {
		close(p.started)
		<-p.release
	}
	if p.off >= len(p.data) {
		return 0, io.EOF
	}
	n := copy(b, p.data[p.off:])
	p.off += n
	return n, nil
}

func TestConcurrentCommitOneWins(t *testing.T) {
	t.Parallel()
	st, dir := openEmbeddedStore(t)
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1024, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteStagedObjectForTest(ctx, st, "acme", up.StagingKey, []byte("hello"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var keys atomic.Int32
	var notFound atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := st.Commit(ctx, up.StagingKey)
			if err != nil {
				if core.CodeOf(err) == core.CodeNotFound {
					notFound.Add(1)
				} else {
					t.Errorf("commit: %v", err)
				}
				return
			}
			if k != ContentKeyOf([]byte("hello")) {
				t.Errorf("key %q", k)
			}
			keys.Add(1)
		}()
	}
	wg.Wait()
	if keys.Load() != 1 || notFound.Load() < 1 {
		t.Fatalf("keys=%d notFound=%d", keys.Load(), notFound.Load())
	}
	_ = dir
}

func TestReplayPUTBlockedWhileCommit403(t *testing.T) {
	t.Parallel()
	st, h := openEmbeddedHandler(t)
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1024, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	putURL := rewriteLocalURL(h, up.URL)
	req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader([]byte("hello")))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("first put: %v %d", err, resp.StatusCode)
	}
	_ = resp.Body.Close()
	pr := &pauseReader{started: make(chan struct{}), release: make(chan struct{}), data: []byte("world")}
	status := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPut, putURL, pr)
		req.Header.Set("Content-Type", "text/plain")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		_ = res.Body.Close()
		status <- res.StatusCode
	}()
	<-pr.started
	key, err := st.Commit(ctx, up.StagingKey)
	if err != nil {
		t.Fatal(err)
	}
	close(pr.release)
	if code := <-status; code != http.StatusForbidden {
		t.Fatalf("replay status %d", code)
	}
	ok, _ := st.Exists(ctx, key)
	if !ok {
		t.Fatal("committed blob missing")
	}
	ok, _ = st.StagedObjectExists(ctx, "acme", up.StagingKey)
	if ok {
		t.Fatal("staged object remains")
	}
}

func TestOversizeReplayKeepsFirstBody(t *testing.T) {
	t.Parallel()
	st, h := openEmbeddedHandler(t)
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 5, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	putURL := rewriteLocalURL(h, up.URL)
	req1, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader([]byte("hello")))
	req1.Header.Set("Content-Type", "text/plain")
	resp, _ := http.DefaultClient.Do(req1)
	_ = resp.Body.Close()
	big := bytes.NewReader(make([]byte, 6))
	req2, _ := http.NewRequest(http.MethodPut, putURL, big)
	req2.Header.Set("Content-Type", "text/plain")
	resp2, _ := http.DefaultClient.Do(req2)
	if resp2.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", resp2.StatusCode)
	}
	_ = resp2.Body.Close()
	key, err := st.Commit(ctx, up.StagingKey)
	if err != nil || key != ContentKeyOf([]byte("hello")) {
		t.Fatalf("commit %q err=%v", key, err)
	}
}

func TestSpendFailedCommitSurfacesStoreError(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	f := &stageFaultBucket{inner: inner, failDeleteStage: true}
	st.bucket = f
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1024, ContentType: "text/plain", TTL: time.Hour})
	if err != nil || !ValidateStagingKey(up.StagingKey) {
		t.Fatalf("start upload: %v key %q", err, up.StagingKey)
	}
	_ = WriteStagedObjectForTest(ctx, st, "acme", up.StagingKey, []byte("hello"), "text/markdown")
	_, err = st.Commit(ctx, up.StagingKey)
	if err == nil || core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
	f.failDeleteStage = false
	f.failWriteCommit = true
	_ = WriteStagedObjectForTest(ctx, st, "acme", up.StagingKey, []byte("hello"), "text/markdown")
	_, err = st.Commit(ctx, up.StagingKey)
	if err == nil || core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("write commit record: %v", err)
	}
}

type stageFaultBucket struct {
	inner           bucketBackend
	failDeleteStage bool
	failWriteCommit bool
}

func (s *stageFaultBucket) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, key)
}

func (s *stageFaultBucket) Delete(ctx context.Context, key string) error {
	if s.failDeleteStage && !stringsHasSuffix(key, ".committed") && !stringsHasSuffix(key, ".slot") {
		return errors.New("delete stage failed")
	}
	return s.inner.Delete(ctx, key)
}

func (s *stageFaultBucket) WriteAll(ctx context.Context, key string, p []byte, opts *blob.WriterOptions) error {
	if s.failWriteCommit && stringsHasSuffix(key, ".committed") {
		return errors.New("write commit failed")
	}
	return s.inner.WriteAll(ctx, key, p, opts)
}

func (s *stageFaultBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return s.inner.NewReader(ctx, key, opts)
}

func (s *stageFaultBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return s.inner.NewWriter(ctx, key, opts)
}

func (s *stageFaultBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return s.inner.Attributes(ctx, key)
}

func (s *stageFaultBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	return s.inner.SignedURL(ctx, key, opts)
}

func (s *stageFaultBucket) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.ListKeys(ctx, prefix)
}

func (s *stageFaultBucket) ListTenantPrefixes(ctx context.Context) ([]string, error) {
	return s.inner.ListTenantPrefixes(ctx)
}

func (s *stageFaultBucket) ListKeysCallback(ctx context.Context, prefix string, fn func(string) error) error {
	return s.inner.ListKeysCallback(ctx, prefix, fn)
}

func (s *stageFaultBucket) Close() error { return s.inner.Close() }

func stringsHasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}

type presignFaultBucket struct {
	inner     bucketBackend
	calls     int
	firstErr  error
	alwaysErr error
}

func (p *presignFaultBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	p.calls++
	if p.alwaysErr != nil {
		return "", p.alwaysErr
	}
	if p.calls == 1 && p.firstErr != nil {
		return "", p.firstErr
	}
	return "https://cloud.example/" + key, nil
}

func (p *presignFaultBucket) Exists(ctx context.Context, key string) (bool, error) {
	return p.inner.Exists(ctx, key)
}
func (p *presignFaultBucket) Delete(ctx context.Context, key string) error {
	return p.inner.Delete(ctx, key)
}
func (p *presignFaultBucket) WriteAll(ctx context.Context, key string, b []byte, opts *blob.WriterOptions) error {
	return p.inner.WriteAll(ctx, key, b, opts)
}
func (p *presignFaultBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return p.inner.NewReader(ctx, key, opts)
}
func (p *presignFaultBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return p.inner.NewWriter(ctx, key, opts)
}
func (p *presignFaultBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return p.inner.Attributes(ctx, key)
}
func (p *presignFaultBucket) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	return p.inner.ListKeys(ctx, prefix)
}
func (p *presignFaultBucket) ListTenantPrefixes(ctx context.Context) ([]string, error) {
	return p.inner.ListTenantPrefixes(ctx)
}
func (p *presignFaultBucket) ListKeysCallback(ctx context.Context, prefix string, fn func(string) error) error {
	return p.inner.ListKeysCallback(ctx, prefix, fn)
}
func (p *presignFaultBucket) Close() error { return p.inner.Close() }

func TestPresignNoRetryOnGenericError(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	st.embedded = false
	p2 := &presignFaultBucket{inner: inner, alwaysErr: errors.New("permission denied")}
	st.bucket = p2
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 100, ContentType: "text/plain", TTL: time.Minute})
	if err == nil || core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("want unavailable got %v", err)
	}
	if p2.calls != 1 {
		t.Fatalf("should not retry: %d calls", p2.calls)
	}
}

func TestPresignFailureRemovesSlot(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	st.embedded = false
	st.bucket = &presignFaultBucket{inner: inner, alwaysErr: gcerrors.ErrPermissionDenied}
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 10, ContentType: "text/plain", TTL: time.Minute})
	if err == nil {
		t.Fatal("want error")
	}
	prefix, _ := inner.ListKeys(ctx, "acme/stage/")
	if len(prefix) != 0 {
		t.Fatalf("slot left behind: %v", prefix)
	}
}

func TestHandlerMaxInt64EmptyBodyNotAccepted(t *testing.T) {
	t.Parallel()
	st, h := openEmbeddedHandler(t)
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: math.MaxInt64, ContentType: "application/octet-stream", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	putURL := rewriteLocalURL(h, up.URL)
	req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(nil))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ok, _ := st.StagedObjectExists(ctx, "acme", up.StagingKey); !ok {
		t.Fatal("expected empty staged object")
	}
	key, err := st.Commit(ctx, up.StagingKey)
	if err != nil || key != ContentKeyOf(nil) {
		t.Fatalf("commit %q err=%v", key, err)
	}
}

func TestCommitMaxInt64DoesNotTruncate(t *testing.T) {
	t.Parallel()
	st, _ := openEmbeddedStore(t)
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: math.MaxInt64, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("x")
	if err := WriteStagedObjectForTest(ctx, st, "acme", up.StagingKey, body, "text/plain"); err != nil {
		t.Fatal(err)
	}
	key, err := st.Commit(ctx, up.StagingKey)
	if err != nil || key != ContentKeyOf(body) {
		t.Fatalf("commit %v key %q", err, key)
	}
}

func TestSweepSlotKeepsMetadataWhenStageDeleteFails(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	st.embedded = true
	st.maxTTL = time.Hour
	clock := coretest.NewFakeClock(time.Unix(1700000000, 0))
	st.clock = clock
	fb := &deleteFailBucket{inner: inner, failStageDelete: true}
	st.bucket = fb
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 10, ContentType: "text/plain", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_ = WriteStagedObjectForTest(ctx, st, "acme", up.StagingKey, []byte("x"), "text/plain")
	clock.Advance(2 * time.Minute)
	if err := st.SweepExpiredStagingSlots(ctx); err == nil {
		t.Fatal("want sweep error")
	}
	mp, _ := slotMetaPath("acme", up.StagingKey)
	ok, _ := st.bucket.Exists(ctx, mp)
	if !ok {
		t.Fatal("slot metadata should remain for retry")
	}
}

type deleteFailBucket struct {
	inner           bucketBackend
	failStageDelete bool
}

func (d *deleteFailBucket) isStagingObject(key string) bool {
	return strings.Contains(key, "/stage/stage_") && !stringsHasSuffix(key, ".slot") && !stringsHasSuffix(key, ".committed")
}

func (d *deleteFailBucket) Exists(ctx context.Context, key string) (bool, error) {
	return d.inner.Exists(ctx, key)
}

func (d *deleteFailBucket) Delete(ctx context.Context, key string) error {
	if d.failStageDelete && d.isStagingObject(key) {
		return errors.New("stage delete failed")
	}
	return d.inner.Delete(ctx, key)
}

func (d *deleteFailBucket) WriteAll(ctx context.Context, key string, p []byte, opts *blob.WriterOptions) error {
	return d.inner.WriteAll(ctx, key, p, opts)
}

func (d *deleteFailBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return d.inner.NewReader(ctx, key, opts)
}

func (d *deleteFailBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return d.inner.NewWriter(ctx, key, opts)
}

func (d *deleteFailBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return d.inner.Attributes(ctx, key)
}

func (d *deleteFailBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	return d.inner.SignedURL(ctx, key, opts)
}

func (d *deleteFailBucket) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	return d.inner.ListKeys(ctx, prefix)
}

func (d *deleteFailBucket) ListTenantPrefixes(ctx context.Context) ([]string, error) {
	return d.inner.ListTenantPrefixes(ctx)
}

func (d *deleteFailBucket) ListKeysCallback(ctx context.Context, prefix string, fn func(string) error) error {
	return d.inner.ListKeysCallback(ctx, prefix, fn)
}

func (d *deleteFailBucket) Close() error { return d.inner.Close() }

func openEmbeddedStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	raw := (&url.URL{Scheme: "file", Path: dir}).String()
	st, _, err := Open(context.Background(), raw, Options{
		Clock:      coretest.NewFakeClock(time.Unix(1700000000, 0)),
		BaseURL:    "http://blob.test/blob",
		SignKey:    []byte("test-signing-key-32-bytes-long"),
		RandReader: coretest.SeededRand(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, dir
}

func openEmbeddedHandler(t *testing.T) (*Store, *httptest.Server) {
	t.Helper()
	st, _ := openEmbeddedStore(t)
	srv := httptest.NewServer(newEmbeddedHandler(st))
	return st, srv
}

func rewriteLocalURL(srv *httptest.Server, raw string) string {
	u, _ := url.Parse(raw)
	u.Scheme = "http"
	u.Host = srv.Listener.Addr().String()
	return u.String()
}

func TestSweepListsStagePrefixOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b, err := fileblob.OpenBucket(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	inner := wrapBucket(b)
	cb := &countListBucket{inner: inner}
	clock := coretest.NewFakeClock(time.Unix(1700000000, 0))
	st := &Store{
		bucket: cb, clock: clock, embedded: true, maxTTL: time.Hour, rand: coretest.SeededRand(1),
	}
	ctx := core.WithTenant(context.Background(), "acme")
	_, _ = st.StartUpload(ctx, platform.UploadOptions{MaxSize: 10, ContentType: "text/plain", TTL: time.Minute})
	hex := ContentKeyOf([]byte("blob"))[len("sha256:"):]
	_ = st.bucket.WriteAll(context.Background(), "acme/"+hex, []byte("blob"), nil)
	clock.Advance(2 * time.Hour)
	if err := st.SweepExpiredStagingSlots(ctx); err != nil {
		t.Fatal(err)
	}
	if cb.prefixes < 1 {
		t.Fatal("expected tenant prefix list")
	}
	for _, p := range cb.stagePrefixes {
		if p != "acme/stage/" {
			t.Fatalf("listed %q", p)
		}
	}
}

type countListBucket struct {
	inner         bucketBackend
	prefixes      int
	stagePrefixes []string
}

func (c *countListBucket) ListTenantPrefixes(ctx context.Context) ([]string, error) {
	c.prefixes++
	return c.inner.ListTenantPrefixes(ctx)
}

func (c *countListBucket) ListKeysCallback(ctx context.Context, prefix string, fn func(string) error) error {
	c.stagePrefixes = append(c.stagePrefixes, prefix)
	return c.inner.ListKeysCallback(ctx, prefix, fn)
}

func (c *countListBucket) Exists(ctx context.Context, key string) (bool, error) {
	return c.inner.Exists(ctx, key)
}
func (c *countListBucket) Delete(ctx context.Context, key string) error {
	return c.inner.Delete(ctx, key)
}
func (c *countListBucket) WriteAll(ctx context.Context, key string, p []byte, opts *blob.WriterOptions) error {
	return c.inner.WriteAll(ctx, key, p, opts)
}
func (c *countListBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return c.inner.NewReader(ctx, key, opts)
}
func (c *countListBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return c.inner.NewWriter(ctx, key, opts)
}
func (c *countListBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return c.inner.Attributes(ctx, key)
}
func (c *countListBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	return c.inner.SignedURL(ctx, key, opts)
}
func (c *countListBucket) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	return c.inner.ListKeys(ctx, prefix)
}
func (c *countListBucket) Close() error { return c.inner.Close() }

func TestSweepRetryAfterListFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b, _ := fileblob.OpenBucket(dir, nil)
	inner := wrapBucket(b)
	fb := &listFailBucket{inner: inner, fail: true}
	clock := coretest.NewFakeClock(time.Unix(1700000000, 0))
	st := &Store{bucket: fb, clock: clock, embedded: true, maxTTL: 7 * 24 * time.Hour, rand: coretest.SeededRand(1)}
	ctx := context.Background()
	st.maybeSweep(ctx)
	if !st.lastSweep.IsZero() {
		t.Fatal("lastSweep must not advance on failed sweep")
	}
	fb.fail = false
	clock.Advance(sweepRetryWait - time.Second)
	st.maybeSweep(ctx)
	if !st.lastSweep.IsZero() {
		t.Fatal("sweep retried too early")
	}
	clock.Advance(2 * time.Second)
	st.maybeSweep(ctx)
	if st.lastSweep.IsZero() {
		t.Fatal("sweep should succeed after retry window")
	}
}

type listFailBucket struct {
	inner bucketBackend
	fail  bool
}

func (l *listFailBucket) ListTenantPrefixes(ctx context.Context) ([]string, error) {
	if l.fail {
		return nil, fmt.Errorf("list failed")
	}
	return l.inner.ListTenantPrefixes(ctx)
}
func (l *listFailBucket) Exists(ctx context.Context, key string) (bool, error) {
	return l.inner.Exists(ctx, key)
}
func (l *listFailBucket) Delete(ctx context.Context, key string) error {
	return l.inner.Delete(ctx, key)
}
func (l *listFailBucket) WriteAll(ctx context.Context, key string, p []byte, opts *blob.WriterOptions) error {
	return l.inner.WriteAll(ctx, key, p, opts)
}
func (l *listFailBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return l.inner.NewReader(ctx, key, opts)
}
func (l *listFailBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return l.inner.NewWriter(ctx, key, opts)
}
func (l *listFailBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return l.inner.Attributes(ctx, key)
}
func (l *listFailBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	return l.inner.SignedURL(ctx, key, opts)
}
func (l *listFailBucket) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	return l.inner.ListKeys(ctx, prefix)
}
func (l *listFailBucket) ListKeysCallback(ctx context.Context, prefix string, fn func(string) error) error {
	return l.inner.ListKeysCallback(ctx, prefix, fn)
}
func (l *listFailBucket) Close() error { return l.inner.Close() }
