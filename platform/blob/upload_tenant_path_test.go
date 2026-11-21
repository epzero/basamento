package blob

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

func TestUploadURLTenantPathEscape(t *testing.T) {
	t.Parallel()
	cases := []string{"a%b", "a?b", "a#b", "a b", "café", "upload"}
	for _, tenant := range cases {
		tenant := tenant
		t.Run(tenant, func(t *testing.T) {
			t.Parallel()
			st, srv := openEmbeddedHandler(t)
			defer func() {
				srv.Close()
				_ = st.Close()
			}()
			ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
			up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 64, ContentType: "text/plain", TTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := url.Parse(up.URL); err != nil {
				t.Fatalf("upload URL invalid: %v", err)
			}
			putURL := rewriteLocalURL(srv, up.URL)
			req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader([]byte("ok")))
			req.Header.Set("Content-Type", "text/plain")
			resp, err := srv.Client().Do(req)
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
