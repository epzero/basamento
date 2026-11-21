package steps

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/epzero/basamento/core"
)

// localStep is the behavior of one scripted step name.
type localStep struct {
	defaultResult json.RawMessage
	defaultErr    *core.Error
	results       map[int]json.RawMessage
	errors        map[int]*core.Error
	tenantResults map[string]json.RawMessage

	barrier *localBarrier
	block   bool
}

func (s *localStep) result(execution int, tenant string) (json.RawMessage, error) {
	if s == nil {
		return json.RawMessage(`{}`), nil
	}
	if s.tenantResults != nil {
		if out, ok := s.tenantResults[tenant]; ok {
			return out, nil
		}
	}
	if s.errors != nil {
		if err := s.errors[execution]; err != nil {
			return nil, err
		}
	}
	if s.results != nil {
		if out, ok := s.results[execution]; ok {
			return out, nil
		}
	}
	if s.defaultErr != nil {
		return nil, s.defaultErr
	}
	if s.defaultResult != nil {
		return s.defaultResult, nil
	}
	return json.RawMessage(`{}`), nil
}

// localBarrier releases the scripted steps of a fan-out only when all of them
// are executing at the same time (REQ-RT-019).
type localBarrier struct {
	mu      sync.Mutex
	want    int
	arrived int
	changed chan struct{}
}

func newLocalBarrier(want int) *localBarrier {
	return &localBarrier{want: want, changed: make(chan struct{})}
}

func (b *localBarrier) enter() {
	b.mu.Lock()
	b.arrived++
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

func (b *localBarrier) wait(ctx context.Context) error {
	for {
		b.mu.Lock()
		done := b.arrived >= b.want
		changed := b.changed
		b.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
