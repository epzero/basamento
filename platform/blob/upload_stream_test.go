package blob

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"testing"
	"time"

	gcblob "gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

type repeatLimitedReader struct {
	pattern []byte
	limit   int64
	sent    int64
}

func (r *repeatLimitedReader) Read(p []byte) (int, error) {
	if r.sent >= r.limit {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && r.sent < r.limit {
		p[n] = r.pattern[r.sent%int64(len(r.pattern))]
		r.sent++
		n++
	}
	if r.sent >= r.limit {
		return n, io.EOF
	}
	return n, nil
}

func TestUploadMemoryDoesNotScaleWithBodySize(t *testing.T) {
	const (
		maxStaging = 64 * 1024 * 1024
		bodyLen    = 32 * 1024 * 1024
		memBudget  = 8 * 1024 * 1024
	)
	st, dir := openEmbeddedStore(t)
	srv := httptest.NewServer(newEmbeddedHandler(st))
	defer func() {
		_ = st.Close()
	}()
	defer srv.Close()
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{
		MaxSize:     maxStaging,
		ContentType: "application/octet-stream",
		TTL:         time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	putURL := rewriteLocalURL(srv, up.URL)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	body := &repeatLimitedReader{pattern: []byte{1, 2, 3, 4}, limit: bodyLen}
	req, err := http.NewRequest(http.MethodPut, putURL, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = -1
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if d := int64(after.TotalAlloc) - int64(before.TotalAlloc); d > memBudget {
		t.Fatalf("TotalAlloc delta %d bytes, want <= %d", d, memBudget)
	}
	u := (&url.URL{Scheme: "file", Path: dir})
	q := u.Query()
	q.Set("no_tmp_dir", "true")
	u.RawQuery = q.Encode()
	cb, err := gcblob.OpenBucket(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cb.Close() }()
	attrs, err := cb.Attributes(ctx, "acme/stage/"+up.StagingKey)
	if err != nil {
		t.Fatal(err)
	}
	if attrs.Size != bodyLen {
		t.Fatalf("staged size %d, want %d", attrs.Size, bodyLen)
	}
}
