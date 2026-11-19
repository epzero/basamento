package blob_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/internal/bdd/s3sim"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/blob"
)

func TestS3UploadSignedURL(t *testing.T) {
	s3 := s3sim.Start()
	defer s3.Close()
	_ = os.Setenv("AWS_ACCESS_KEY_ID", "TEST")
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "TEST")
	st, _, err := blob.Open(context.Background(), s3.URL, blob.Options{Clock: coretest.NewFakeClock(time.Unix(1700000000, 0)), RandReader: coretest.SeededRand(1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	u, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1024, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("URL", u.URL)
	if u.URL == "" {
		t.Fatal("empty url")
	}
}
