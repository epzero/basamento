package blob

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	"github.com/epzero/basamento/core"
)

// TestDeleteSerializesWithPut is a stress smoke test under random interleaving; it does not
// reproduce the Delete/Put sidecar race (see TestPutDeleteHoldPerKeyLock).
func TestDeleteSerializesWithPut(t *testing.T) {
	t.Parallel()
	st, _, dir := openFileStore(t)
	ctx := core.WithTenant(context.Background(), "acme")
	body := []byte("race-delete-put-bytes-content")
	key := ContentKeyOf(body)
	var wg sync.WaitGroup
	const rounds = 32
	wg.Add(rounds * 3)
	for i := 0; i < rounds; i++ {
		go func() {
			defer wg.Done()
			_ = st.Put(ctx, key, bytes.NewReader(body), "text/plain")
		}()
		go func() {
			defer wg.Done()
			_ = st.Delete(ctx, key)
		}()
		go func() {
			defer wg.Done()
			key2 := ContentKeyOf([]byte{byte('a' + i%10)})
			_ = st.Put(ctx, key2, bytes.NewReader([]byte{byte('a' + i%10)}), "text/plain")
		}()
	}
	wg.Wait()
	ok, err := st.Exists(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		rc, err := st.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		_ = rc.Close()
		if !bytes.Equal(got, body) {
			t.Fatal("object without matching body")
		}
		if ct, _ := st.ContentType(ctx, key); ct == "" {
			t.Fatal("object without sidecar content type")
		}
	}
	mp, _ := metaPath("acme", key)
	metaOK, err := st.bucket.Exists(ctx, mp)
	if err != nil {
		t.Fatal(err)
	}
	if ok != metaOK {
		t.Fatalf("object/meta mismatch: object=%v meta=%v dir=%s", ok, metaOK, dir)
	}
	if st.putLocks.activeKeys() != 0 {
		t.Fatalf("locks not empty: %d", st.putLocks.activeKeys())
	}
}
