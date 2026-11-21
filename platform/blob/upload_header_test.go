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

func TestPUTWithoutContentTypeRejected(t *testing.T) {
	t.Parallel()
	for _, ct := range []string{"text/plain", "application/octet-stream"} {
		ct := ct
		t.Run(ct, func(t *testing.T) {
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
			res, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusUnsupportedMediaType {
				t.Fatalf("PUT without Content-Type %d want 415", res.StatusCode)
			}
		})
	}
}
