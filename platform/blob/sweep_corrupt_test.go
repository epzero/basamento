package blob

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
)

func TestSweepCorruptSlotDoesNotBlockExpired(t *testing.T) {
	t.Parallel()
	st, _ := openEmbeddedStore(t)
	defer func() { _ = st.Close() }()
	clock := coretest.NewFakeClock(time.Unix(1700000000, 0))
	st.clock = clock
	st.maxTTL = time.Hour
	ctx := core.WithTenant(context.Background(), "acme")
	expired, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 10, ContentType: "text/plain", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_ = writeStagedObjectForTest(ctx, st, "acme", expired.StagingKey, []byte("old"), "text/plain")
	fresh, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 10, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	corruptSlot, _ := slotMetaPath("acme", fresh.StagingKey)
	if err := st.bucket.WriteAll(ctx, corruptSlot, []byte("{not json"), nil); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	err = st.runSweep(ctx)
	if err == nil {
		t.Fatal("want corrupt slot read error")
	}
	ok, _ := stagedObjectExistsForTest(ctx, st, "acme", expired.StagingKey)
	if ok {
		t.Fatal("expired staging object should be swept")
	}
	okFresh, _ := st.bucket.Exists(ctx, corruptSlot)
	if !okFresh {
		t.Fatal("corrupt slot metadata should remain for a later sweep")
	}
}
