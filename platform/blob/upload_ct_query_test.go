package blob

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

func TestUploadPUTWithPlusContentType(t *testing.T) {
	t.Parallel()
	st, srv := openEmbeddedHandler(t)
	defer func() {
		srv.Close()
		_ = st.Close()
	}()
	ctx := core.WithTenant(context.Background(), "acme")
	const ct = "application/ld+json"
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1024, ContentType: ct, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	putURL := rewriteLocalURL(srv, up.URL)
	req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader([]byte("body")))
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT %d want 201 for bound type %q", resp.StatusCode, ct)
	}
}

func TestUploadQueryContentTypeRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []string{
		"application/ld+json",
		"application/vnd.api+json",
		"application/x+y",
		"application/x&y",
		"application/x%yy",
		"application/x~z",
	}
	for _, ct := range cases {
		ct := ct
		if !validBoundContentType(ct) {
			t.Logf("skip invalid bound type %q", ct)
			continue
		}
		t.Run(ct, func(t *testing.T) {
			t.Parallel()
			st, srv := openEmbeddedHandler(t)
			defer func() {
				srv.Close()
				_ = st.Close()
			}()
			ctx := core.WithTenant(context.Background(), "acme")
			up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 64, ContentType: ct, TTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			putURL := rewriteLocalURL(srv, up.URL)
			req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader([]byte("x")))
			req.Header.Set("Content-Type", ct)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("PUT %d want 201", resp.StatusCode)
			}
		})
	}
}
