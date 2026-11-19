package blob

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
)

func TestSweepIgnoresStrayTmpAndSweepsExpired(t *testing.T) {
	t.Parallel()
	st, dir := openEmbeddedStore(t)
	defer func() { _ = st.Close() }()
	_ = dir
	clock := coretest.NewFakeClock(time.Unix(1700000000, 0))
	st.clock = clock
	st.maxTTL = time.Hour
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 10, ContentType: "text/plain", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_ = writeStagedObjectForTest(ctx, st, "acme", up.StagingKey, []byte("x"), "text/plain")
	tmpKey := "acme/stage/" + up.StagingKey + ".1a2b3c.tmp"
	if err := st.bucket.WriteAll(ctx, tmpKey, []byte("partial"), nil); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := st.runSweep(ctx); err != nil {
		t.Fatal(err)
	}
	ok, _ := stagedObjectExistsForTest(ctx, st, "acme", up.StagingKey)
	if ok {
		t.Fatal("expired staging object should be swept")
	}
	ok, _ = st.bucket.Exists(ctx, tmpKey)
	if !ok {
		t.Fatal("stray tmp file must not be deleted by sweep")
	}
}
