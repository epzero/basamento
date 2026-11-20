package bus

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func TestOpenEmbedded(t *testing.T) {
	dir, err := os.MkdirTemp("", "bus-open-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	st, err := OpenEmbedded(context.Background(), dir, Options{Clock: clk})
	if err != nil {
		var pe *core.Error
		if errors.As(err, &pe) {
			t.Fatalf("open: %s details=%v", pe.Message, pe.Details)
		}
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	seq, err := st.Publish(context.Background(), "run.r1.deltas", []byte("m1"))
	if err != nil {
		var pe *core.Error
		if errors.As(err, &pe) {
			t.Fatalf("publish: %s details=%v", pe.Message, pe.Details)
		}
		t.Fatalf("publish: %v", err)
	}
	if seq == 0 {
		t.Fatal("zero seq")
	}
}
