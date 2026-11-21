package local

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/runtime"
)

type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(string) slog.Handler { return h }

func (h *captureHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.records)
}

func (h *captureHandler) last() slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) == 0 {
		return slog.Record{}
	}
	return h.records[len(h.records)-1]
}

func attrString(rec slog.Record, key string) string {
	var v string
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v = a.Value.String()
		}
		return true
	})
	return v
}

func newLoggedFixture(t *testing.T, log *captureHandler) *fixture {
	t.Helper()
	clock := coretest.NewFakeClock(time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	f := &fixture{clock: clock, host: testHost{}}
	logger := slog.New(log)
	var err error
	f.rt, err = New(Options{Clock: clock, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rt.RegisterFlowHost(f.host.Run); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestStepPanicLogsRecoveredValue(t *testing.T) {
	log := &captureHandler{}
	f := newLoggedFixture(t, log)
	defer f.close(t)
	if err := f.rt.RegisterStep("leak", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic(secretToken)
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		_ = dc.Step("leak", nil, runtime.StepOptions{}).Get(nil)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "parent", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	if log.count() != 1 {
		t.Fatalf("log records %d, want 1", log.count())
	}
	rec := log.last()
	if !strings.Contains(attrString(rec, "panic"), secretToken) {
		t.Fatalf("log panic field missing token")
	}
	if attrString(rec, "stack") == "" {
		t.Fatal("log stack empty")
	}
	if attrString(rec, "step") != "leak" {
		t.Fatalf("log step %q", attrString(rec, "step"))
	}
	if attrString(rec, "tenant") != "acme" || attrString(rec, "run") != "r1" {
		t.Fatalf("log tenant/run")
	}
}

func TestFlowPanicLogsRecoveredValue(t *testing.T) {
	log := &captureHandler{}
	f := newLoggedFixture(t, log)
	defer f.close(t)
	f.flow("panic-flow", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		panic(secretToken)
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Child(runtime.FlowRef{Name: "panic-flow"}, nil, runtime.ChildOptions{ID: "c1"}).Get(nil)
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	f.waitState(t, "acme", "p1", stateFailed)
	if log.count() != 1 {
		t.Fatalf("log records %d, want 1", log.count())
	}
	rec := log.last()
	if !strings.Contains(attrString(rec, "panic"), secretToken) {
		t.Fatalf("log panic missing token")
	}
	if attrString(rec, "tenant") != "acme" || attrString(rec, "run") != "c1" {
		t.Fatalf("log tenant/run")
	}
}
