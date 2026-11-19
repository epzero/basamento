package blob

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gocloud.dev/gcerrors"

	"github.com/epzero/basamento/core"
)

func assertPutLockHeld(t *testing.T, st *Store, lockKey string) {
	t.Helper()
	st.putLocks.mu.Lock()
	defer st.putLocks.mu.Unlock()
	ent := st.putLocks.m[lockKey]
	if ent == nil || ent.ref < 1 {
		t.Fatalf("per-key lock %q not held (ref=%v)", lockKey, entRef(ent))
	}
}

func entRef(ent *keyedEntry) int {
	if ent == nil {
		return 0
	}
	return ent.ref
}

func contentLockKey(tid core.TenantID, key string) string {
	hex, err := HexOf(key)
	if err != nil {
		panic(err)
	}
	return string(tid) + "/" + hex
}

func lockKeyFromBucketKey(bucketKey string) string {
	return strings.TrimSuffix(bucketKey, ".meta")
}

// TestPutDeleteHoldPerKeyLock checks Store.Put and Store.Delete hold the per-key lock
// for every mutating bucket operation on that content key (deterministic; not a race test).
func TestPutDeleteHoldPerKeyLock(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	body := []byte("lock-guard-bytes-for-put-delete")
	key := ContentKeyOf(body)
	lockKey := contentLockKey("acme", key)
	objPath, err := objectPath("acme", key)
	if err != nil {
		t.Fatal(err)
	}
	metaKey, err := metaPath("acme", key)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("PutNew", func(t *testing.T) {
		var st *Store
		var sawObj, sawMeta bool
		b := newTestBucket(0, gcerrors.Unknown)
		b.onOp = func(op, k string) {
			if op != "NewTypedWriter" {
				return
			}
			if lockKeyFromBucketKey(k) != lockKey {
				return
			}
			assertPutLockHeld(t, st, lockKey)
			switch k {
			case objPath:
				sawObj = true
			case metaKey:
				sawMeta = true
			}
		}
		st = openStoreWithTestBucket(t, b)
		if err := st.Put(ctx, key, bytes.NewReader(body), "text/plain"); err != nil {
			t.Fatal(err)
		}
		if !sawObj || !sawMeta {
			t.Fatalf("want object and sidecar writers; obj=%v meta=%v", sawObj, sawMeta)
		}
	})

	t.Run("PutExisting", func(t *testing.T) {
		var st *Store
		b := newTestBucket(0, gcerrors.Unknown)
		st = openStoreWithTestBucket(t, b)
		if err := st.Put(ctx, key, bytes.NewReader(body), "text/plain"); err != nil {
			t.Fatal(err)
		}
		b.onOp = func(op, k string) {
			t.Fatalf("putExisting must not mutate bucket, got %s %s", op, k)
		}
		if err := st.Put(ctx, key, bytes.NewReader(body), "text/other"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("PutFailMidStream", func(t *testing.T) {
		var st *Store
		var sawObj bool
		failBody := make([]byte, 256)
		for i := range failBody {
			failBody[i] = byte(i)
		}
		failKey := ContentKeyOf(failBody)
		failLock := contentLockKey("acme", failKey)
		failObjPath, err := objectPath("acme", failKey)
		if err != nil {
			t.Fatal(err)
		}
		b := newTestBucket(2, gcerrors.Unknown)
		b.onOp = func(op, k string) {
			if op != "NewTypedWriter" || lockKeyFromBucketKey(k) != failLock {
				return
			}
			assertPutLockHeld(t, st, failLock)
			if k == failObjPath {
				sawObj = true
			}
		}
		st = openStoreWithTestBucket(t, b)
		putErr := st.Put(ctx, failKey, &chunkReader{b: failBody, chunk: 64}, "text/plain")
		if putErr == nil || core.CodeOf(putErr) != core.CodeUnavailable {
			t.Fatalf("want unavailable, got %v", putErr)
		}
		if !sawObj {
			t.Fatal("object writer hook did not run")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		var st *Store
		var delObj, delMeta bool
		b := newTestBucket(0, gcerrors.Unknown)
		st = openStoreWithTestBucket(t, b)
		delKey := ContentKeyOf([]byte("delete-lock-guard-bytes"))
		delLock := contentLockKey("acme", delKey)
		if err := st.Put(ctx, delKey, bytes.NewReader([]byte("delete-lock-guard-bytes")), "text/plain"); err != nil {
			t.Fatal(err)
		}
		b.onOp = func(op, k string) {
			if op != "Delete" {
				return
			}
			if lockKeyFromBucketKey(k) != delLock {
				return
			}
			assertPutLockHeld(t, st, delLock)
			if k == delLock {
				delObj = true
			}
			if strings.HasSuffix(k, ".meta") {
				delMeta = true
			}
		}
		if err := st.Delete(ctx, delKey); err != nil {
			t.Fatal(err)
		}
		if !delObj || !delMeta {
			t.Fatalf("want object and sidecar deletes; obj=%v meta=%v", delObj, delMeta)
		}
	})
}
