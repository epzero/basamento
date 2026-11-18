package blob

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"gocloud.dev/blob"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

// writeTapBucket is a test wrapper that pairs with stagingFirstWriteHook on Store.
type writeTapBucket struct {
	bucketBackend
	sawWrite chan struct{}
	store    *Store
}

func (w *writeTapBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	bw, err := w.bucketBackend.NewWriter(ctx, key, opts)
	if err != nil {
		return nil, err
	}
	if w.store != nil && isStagingDataKey(key) {
		ch := w.sawWrite
		var once sync.Once
		w.store.stagingFirstWriteHook = func() { once.Do(func() { close(ch) }) }
	}
	return bw, nil
}

func isStagingDataKey(key string) bool {
	return stringsContains(key, "/stage/stage_") && !stringsHasSuffix(key, ".slot") && !stringsHasSuffix(key, ".committed")
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type stallBody struct {
	first    []byte
	rest     []byte
	gate     chan struct{}
	started  chan struct{}
	gateWait sync.Once
}

func (s *stallBody) Read(p []byte) (int, error) {
	if len(s.first) > 0 {
		n := copy(p, s.first)
		s.first = nil
		close(s.started)
		return n, nil
	}
	s.gateWait.Do(func() { <-s.gate })
	if len(s.rest) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.rest)
	s.rest = s.rest[n:]
	return n, nil
}

func TestUploadStreamsBeforeBodyEnds(t *testing.T) {
	t.Parallel()
	st, srv := openEmbeddedHandler(t)
	defer srv.Close()
	defer func() {
		st.stagingFirstWriteHook = nil
		_ = st.Close()
	}()
	inner := st.bucket
	tap := &writeTapBucket{bucketBackend: inner, sawWrite: make(chan struct{}), store: st}
	st.bucket = tap

	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1024, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	putURL := rewriteLocalURL(srv, up.URL)
	gate := make(chan struct{})
	body := &stallBody{first: []byte("hel"), rest: []byte("lo"), gate: gate, started: make(chan struct{})}
	status := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPut, putURL, body)
		req.Header.Set("Content-Type", "text/plain")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		_ = res.Body.Close()
		status <- res.StatusCode
	}()
	<-body.started
	<-tap.sawWrite
	close(gate)
	if code := <-status; code != http.StatusCreated {
		t.Fatalf("status %d", code)
	}
	got, _, err := st.ReadStagedObject(ctx, "acme", up.StagingKey)
	if err != nil || string(got) != "hello" {
		t.Fatalf("staged %q err=%v", got, err)
	}
}

func TestChunkedOversizeKeepsPriorStaged(t *testing.T) {
	t.Parallel()
	st, srv := openEmbeddedHandler(t)
	defer func() { _ = st.Close() }()
	defer srv.Close()
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 5, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	putURL := rewriteLocalURL(srv, up.URL)
	req1, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader([]byte("hello")))
	req1.Header.Set("Content-Type", "text/plain")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil || resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first put: %v %d", err, resp1.StatusCode)
	}
	_ = resp1.Body.Close()
	req2, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(make([]byte, 6)))
	req2.Header.Set("Content-Type", "text/plain")
	req2.ContentLength = -1
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", resp2.StatusCode)
	}
	key, err := st.Commit(ctx, up.StagingKey)
	if err != nil || key != ContentKeyOf([]byte("hello")) {
		t.Fatalf("commit %q err=%v", key, err)
	}
}
