package blob

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
)

func TestSweepSkipsSlotWhenLockHeld(t *testing.T) {
	t.Parallel()
	st, _ := openEmbeddedStore(t)
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 64, ContentType: "text/plain", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	tid := core.TenantID("acme")
	unlock := st.stageLocks.lock(stageLockKey(tid, up.StagingKey))
	clock := st.clock.(*coretest.FakeClock)
	clock.Advance(2 * time.Hour)
	if err := st.runSweep(ctx); err != nil {
		t.Fatal(err)
	}
	stagePath, _ := stageObjectPath(tid, up.StagingKey)
	mp, _ := slotMetaPath(tid, up.StagingKey)
	ok, _ := st.bucket.Exists(ctx, mp)
	if !ok {
		t.Fatal("slot metadata must remain while lock is held")
	}
	ok, _ = st.bucket.Exists(ctx, stagePath)
	_ = ok
	unlock()
	clock.Advance(2 * time.Hour)
	if err := st.runSweep(ctx); err != nil {
		t.Fatal(err)
	}
	ok, _ = st.bucket.Exists(ctx, mp)
	if ok {
		t.Fatal("slot metadata should be swept after lock released")
	}
}

func TestSweepRecoversOrphanStagedWithoutMeta(t *testing.T) {
	t.Parallel()
	st, _ := openEmbeddedStore(t)
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 64, ContentType: "text/plain", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	tid := core.TenantID("acme")
	if err := writeStagedObjectForTest(ctx, st, "acme", up.StagingKey, []byte("orphan"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	mp, _ := slotMetaPath(tid, up.StagingKey)
	if err := st.bucket.Delete(ctx, mp); err != nil {
		t.Fatal(err)
	}
	stagePath, _ := stageObjectPath(tid, up.StagingKey)
	clock := st.clock.(*coretest.FakeClock)
	clock.Advance(2 * time.Hour)
	if err := st.runSweep(ctx); err != nil {
		t.Fatal(err)
	}
	ok, _ := st.bucket.Exists(ctx, stagePath)
	if ok {
		t.Fatal("orphan staged object should be removed")
	}
}

func TestKeyedMuTryLockBusy(t *testing.T) {
	t.Parallel()
	var k keyedMu
	held := make(chan struct{})
	release := make(chan struct{})
	released := make(chan struct{})
	go func() {
		unlock := k.lock("slot")
		close(held)
		<-release
		unlock()
		close(released)
	}()
	<-held
	if _, ok := k.tryLock("slot"); ok {
		t.Fatal("tryLock should fail while key is locked")
	}
	close(release)
	<-released
	if unlock, ok := k.tryLock("slot"); !ok {
		t.Fatal("tryLock should succeed after release")
	} else {
		unlock()
	}
}
