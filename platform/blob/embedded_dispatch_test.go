package blob

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

func TestEmbeddedTenantNamedUploadCanReadAndUpload(t *testing.T) {
	t.Parallel()
	for _, tenant := range []string{"upload", "blob"} {
		tenant := tenant
		t.Run(tenant, func(t *testing.T) {
			t.Parallel()
			st, srv := openEmbeddedHandler(t)
			defer func() {
				srv.Close()
				_ = st.Close()
			}()
			ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
			body := []byte("payload-" + tenant)
			key := ContentKeyOf(body)
			if err := st.Put(ctx, key, bytes.NewReader(body), "text/plain"); err != nil {
				t.Fatal(err)
			}
			readURL, err := st.SignedURL(ctx, key, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			readURL = rewriteLocalURL(srv, readURL)
			res, err := http.Get(readURL)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET read %d want 200", res.StatusCode)
			}
			got, _ := io.ReadAll(res.Body)
			if !bytes.Equal(got, body) {
				t.Fatalf("read body mismatch")
			}
			up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 256, ContentType: "text/plain", TTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			putURL := rewriteLocalURL(srv, up.URL)
			req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader([]byte("staged")))
			req.Header.Set("Content-Type", "text/plain")
			pres, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = pres.Body.Close()
			if pres.StatusCode != http.StatusCreated {
				t.Fatalf("PUT upload %d want 201", pres.StatusCode)
			}
		})
	}
}

func TestEmbeddedContentKeyShapedStageStillUploads(t *testing.T) {
	t.Parallel()
	st, srv := openEmbeddedHandler(t)
	defer func() {
		srv.Close()
		_ = st.Close()
	}()
	ctx := core.WithTenant(context.Background(), "acme")
	body := []byte("via-put")
	key := ContentKeyOf(body)
	if err := st.Put(ctx, key, bytes.NewReader(body), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	readURL, err := st.SignedURL(ctx, key, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	readURL = rewriteLocalURL(srv, readURL)
	res, err := http.Get(readURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %d want 200 for content-shaped key", res.StatusCode)
	}
}
