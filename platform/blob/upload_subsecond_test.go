package blob

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
)

func openEmbeddedStoreAt(t *testing.T, start time.Time) (*Store, *coretest.FakeClock) {
	t.Helper()
	clock := coretest.NewFakeClock(start)
	dir := t.TempDir()
	raw := (&url.URL{Scheme: "file", Path: dir}).String()
	st, _, err := Open(context.Background(), raw, Options{
		Clock:      clock,
		BaseURL:    "http://blob.test/blob",
		SignKey:    []byte("test-signing-key-32-bytes-long!!"),
		RandReader: coretest.SeededRand(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, clock
}

func TestSubSecondUploadSlotLiveUntilTTL(t *testing.T) {
	t.Parallel()
	start := time.Unix(1700000000, 700_000_000)
	st, clock := openEmbeddedStoreAt(t, start)
	defer func() { _ = st.Close() }()
	srv := httptest.NewServer(newEmbeddedHandler(st))
	defer srv.Close()
	ctx := core.WithTenant(context.Background(), "acme")
	const ttl = 400 * time.Millisecond
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 64, ContentType: "text/plain", TTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	meta, ok, err := st.readSlot(ctx, core.TenantID("acme"), up.StagingKey)
	if err != nil || !ok || meta.expired(clock.Now()) {
		t.Fatalf("slot expired at birth: ok=%v err=%v", ok, err)
	}
	putURL := rewriteLocalURL(srv, up.URL)
	req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader([]byte("x")))
	req.Header.Set("Content-Type", "text/plain")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("PUT %d want 201 within TTL", res.StatusCode)
	}
	if _, err := st.Commit(ctx, up.StagingKey); err != nil {
		t.Fatalf("commit: %v", err)
	}
	up2, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1, ContentType: "text/plain", TTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	meta2, ok, _ := st.readSlot(ctx, core.TenantID("acme"), up2.StagingKey)
	if !ok {
		t.Fatal("slot missing")
	}
	clock.Advance(ttl + time.Millisecond)
	if !meta2.expired(clock.Now()) {
		t.Fatal("slot should expire after TTL")
	}
	if err := st.runSweep(ctx); err != nil {
		t.Fatal(err)
	}
	mp, _ := slotMetaPath("acme", up2.StagingKey)
	ok, _ = st.bucket.Exists(ctx, mp)
	if ok {
		t.Fatal("sweep should delete expired slot")
	}
}
