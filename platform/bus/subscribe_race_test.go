package bus

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestSubscribeCancelOverflowRace(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	subject := "run.r1.deltas"
	for i := 0; i < 50; i++ {
		func() {
			subCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			ch, err := st.Subscribe(subCtx, subject, 0)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				for j := 0; j < 8; j++ {
					_, _ = st.Publish(ctx, subject, []byte{byte(j)})
				}
			}()
			go func() {
				defer wg.Done()
				cancel()
			}()
			wg.Wait()
			for range ch {
			}
		}()
	}
}

func TestPublishOversizeInvalidArgument(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	data := make([]byte, st.publishLimit+1)
	_, err := st.Publish(context.Background(), "run.r1.deltas", data)
	if err == nil || string(core.CodeOf(err)) != "invalid_argument" {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "nats://") {
		t.Fatal("error echoes url")
	}
}
