package eventlog

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
)

var testNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func testLog(t *testing.T, fault Fault) *Log {
	t.Helper()
	dir := t.TempDir()
	store, err := psql.Open(context.Background(), "sqlite:///"+filepath.Join(dir, "kit.db"), Migrations())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log, err := New(store, Options{Clock: coretest.NewFakeClock(testNow), Fault: fault})
	if err != nil {
		t.Fatalf("new log: %v", err)
	}
	return log
}

func tenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

func mustCreateRun(t *testing.T, l *Log, runID, state string) {
	t.Helper()
	if err := l.CreateRun(tenantCtx("acme"), runID, state); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
}

func eventTypes(types ...string) []NewEvent {
	out := make([]NewEvent, len(types))
	for i, typ := range types {
		out[i] = NewEvent{Type: typ, Data: json.RawMessage(`{"i":` + strconv.Itoa(i) + `}`)}
	}
	return out
}

func appendTypes(t *testing.T, l *Log, runID, state string, types ...string) []Event {
	t.Helper()
	out, err := l.AppendChange(tenantCtx("acme"), runID, state, eventTypes(types...))
	if err != nil {
		t.Fatalf("append %v: %v", types, err)
	}
	return out
}

func readAll(t *testing.T, l *Log, runID string) []Event {
	t.Helper()
	events, err := l.Read(tenantCtx("acme"), runID, 0)
	if err != nil {
		t.Fatalf("read %s: %v", runID, err)
	}
	return events
}

// fakeBus records published envelopes and can refuse publishes.
type fakeBus struct {
	mu        sync.Mutex
	envs      []Envelope
	accepted  int
	failAfter int
	down      bool
}

func (b *fakeBus) Publish(_ context.Context, subject string, data []byte) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down || (b.failAfter > 0 && b.accepted >= b.failAfter) {
		return 0, core.NewError(core.CodeUnavailable, "bus down", nil)
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return 0, core.NewError(core.CodeInternal, "bad envelope", nil)
	}
	b.accepted++
	b.envs = append(b.envs, env)
	return uint64(b.accepted), nil
}

func (b *fakeBus) Subscribe(context.Context, string, uint64) (<-chan platform.Message, error) {
	return nil, core.NewError(core.CodeUnsupported, "not used", nil)
}

func (b *fakeBus) LastSeq(context.Context, string) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return uint64(b.accepted), nil
}

func (b *fakeBus) Purge(context.Context, string) error { return nil }

func (b *fakeBus) sequences(runID string) []uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []uint64
	for _, e := range b.envs {
		if e.RunID == runID {
			out = append(out, e.Seq)
		}
	}
	return out
}

func (b *fakeBus) setDown(down bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.down = down
}

func (b *fakeBus) setFailAfter(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failAfter = n
}

func newRelay(t *testing.T, l *Log, bus platform.Bus, fault Fault) *Relay {
	t.Helper()
	r, err := NewRelay(l, bus, RelayOptions{Clock: coretest.NewFakeClock(testNow), Fault: fault})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	return r
}

func faultAt(point string, code core.Code) Fault {
	return func(p string) error {
		if p == point {
			return core.NewError(code, "stopped at "+p, nil)
		}
		return nil
	}
}

func wantSeqs(t *testing.T, got []uint64, want ...uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sequences %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequences %v, want %v", got, want)
		}
	}
}

func wantTypes(t *testing.T, events []Event, want ...string) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d", len(events), len(want))
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event %d type %q, want %q", i, events[i].Type, want[i])
		}
	}
}
