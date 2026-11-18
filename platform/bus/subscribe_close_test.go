package bus

import (
	"context"
	"sync"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestTryRegisterLiveAfterClose(t *testing.T) {
	st := &Store{live: map[*liveSubscription]struct{}{}}
	st.closed.Store(true)
	live := newLiveSubscription(4, func() {}, nil)
	err := st.tryRegisterLive(live)
	if err == nil {
		t.Fatal("expected unavailable")
	}
	if string(core.CodeOf(err)) != string(core.CodeUnavailable) {
		t.Fatalf("got %s", core.CodeOf(err))
	}
	if len(st.live) != 0 {
		t.Fatal("registered after close")
	}
}

func TestLiveAttachEndOrdering(t *testing.T) {
	t.Parallel()
	cc := newStubConsume()
	live := newLiveSubscription(2, func() {}, nil)
	live.end()
	live.attach(cc)
	if cc.stopCount() != 1 {
		t.Fatalf("attach after end stops=%d", cc.stopCount())
	}
	cc2 := newStubConsume()
	live2 := newLiveSubscription(2, func() {}, nil)
	live2.attach(cc2)
	live2.end()
	if cc2.stopCount() != 1 {
		t.Fatalf("end after attach stops=%d", cc2.stopCount())
	}
	cc3 := newStubConsume()
	live3 := newLiveSubscription(2, func() {}, nil)
	live3.end()
	live3.attach(cc3)
	live3.end()
	if cc3.stopCount() != 1 {
		t.Fatalf("double end stops=%d", cc3.stopCount())
	}
}

func TestSubscribeCloseRace(t *testing.T) {
	const goroutines = 24
	for iter := 0; iter < 20; iter++ {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			st, _, _ := openTestEmbedded(t)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < goroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					subCtx, cancel := context.WithCancel(context.Background())
					defer cancel()
					ch, err := st.Subscribe(subCtx, "run.r1.race", 0)
					if err != nil {
						if string(core.CodeOf(err)) != string(core.CodeUnavailable) {
							t.Errorf("subscribe: %v", err)
						}
						return
					}
					for range ch {
					}
				}()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_ = st.Close()
			}()
			close(start)
			wg.Wait()
			if st.liveCount() != 0 {
				t.Fatalf("live=%d", st.liveCount())
			}
		})
	}
}

func (s *Store) liveCount() int {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	return len(s.live)
}
