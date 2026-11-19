package bus

import (
	"context"
	"runtime"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestSubscribeRejectsRaisedFloorAfterConsumer(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	subject := "run.r1.sfloor"
	for i := 0; i < 4; i++ {
		if _, err := st.Publish(ctx, subject, []byte("m")); err != nil {
			t.Fatal(err)
		}
	}
	var gotErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := st.Subscribe(ctx, subject, 1)
		gotErr = err
	}()
	purged := false
	for i := 0; i < 500_000; i++ {
		if err := st.Purge(ctx, subject); err != nil {
			t.Fatal(err)
		}
		purged = true
		runtime.Gosched()
		select {
		case <-done:
			goto check
		default:
		}
	}
	<-done
check:
	if !purged {
		t.Fatal("no purge attempt")
	}
	if gotErr == nil {
		t.Fatal("expected purged")
	}
	if string(core.CodeOf(gotErr)) != string(core.CodeNotFound) {
		t.Fatalf("got %v", gotErr)
	}
}
