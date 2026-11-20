package bus

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestPurgeFloorCorrupt(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	subject := "run.r1.pf"
	key := metaKey(metaPF, subject)
	if _, err := st.meta.Put(ctx, key, []byte{0x01}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LastSeq(ctx, subject); err == nil {
		t.Fatal("expected corrupt floor error")
	} else if string(core.CodeOf(err)) != string(core.CodeInternal) {
		t.Fatalf("LastSeq code %s", core.CodeOf(err))
	}
	if err := st.Purge(ctx, subject); err == nil {
		t.Fatal("expected corrupt floor on purge")
	} else if string(core.CodeOf(err)) != string(core.CodeInternal) {
		t.Fatalf("Purge code %s", core.CodeOf(err))
	}
}

func TestSubscribePurgedAfterConsumer(t *testing.T) {
	if err := subscribePurged(2, 5); err == nil {
		t.Fatal("expected purged")
	}
	if err := subscribePurged(0, 5); err != nil {
		t.Fatalf("after=0 floor 5: %v", err)
	}
	if err := subscribePurged(5, 5); err != nil {
		t.Fatalf("after=floor: %v", err)
	}
}
