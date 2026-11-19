package bus

import (
	"context"
	"sync"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

func TestCloseEndsSubscriptions(t *testing.T) {
	const n = 3
	for iter := 0; iter < n; iter++ {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			st, _, _ := openTestEmbedded(t)
			ctx := context.Background()
			var cancels []context.CancelFunc
			var chans []<-chan struct{}
			for i := 0; i < 4; i++ {
				subCtx, cancel := context.WithCancel(ctx)
				ch, err := st.Subscribe(subCtx, "run.r1.events", 0)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				done := make(chan struct{})
				chans = append(chans, done)
				go func(done chan struct{}, inbox <-chan platform.Message) {
					for {
						_, ok := <-inbox
						if !ok {
							close(done)
							return
						}
					}
				}(done, ch)
				cancels = append(cancels, cancel)
			}
			_ = st.Close()
			var wg sync.WaitGroup
			for _, d := range chans {
				wg.Add(1)
				go func(d <-chan struct{}) {
					defer wg.Done()
					<-d
				}(d)
			}
			wg.Wait()
			_, err := st.Publish(ctx, "run.r1.events", []byte("x"))
			if err == nil || string(core.CodeOf(err)) != string(core.CodeUnavailable) {
				t.Fatalf("publish after close: %v", err)
			}
			for _, c := range cancels {
				c()
			}
		})
	}
}
