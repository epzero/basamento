package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestDurableContextHelpers(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("helpers", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		first := dc.NewID("span")
		second := dc.NewID("span")
		sourceOne := dc.Rand("shuffle")
		sourceTwo := dc.Rand("shuffle")
		if sourceOne != sourceTwo {
			return nil, fmt.Errorf("Rand(key) did not return the same source")
		}
		if err := dc.Emit(runtime.ProgressEvent{Type: "progress", Data: json.RawMessage(`{"n":1}`)}); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{BlobKey: "b1", Seq: 1}); err != nil {
			return nil, err
		}
		if caps := dc.Capabilities(); caps != f.rt.Capabilities() {
			return nil, fmt.Errorf("context capabilities %+v differ from the runtime's", caps)
		}
		return json.RawMessage(fmt.Sprintf(`{"first":%q,"second":%q}`, first, second)), nil
	})
	ref := f.start(t, "acme", "r1", "helpers", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	if out := f.output(t, "acme", "r1"); string(out) != `{"first":"span_1","second":"span_2"}` {
		t.Fatalf("ids %s, want span_1 then span_2", out)
	}
	target := f.rt.lookup(core.TenantID("acme"), "r1")
	if target == nil {
		t.Fatal("run not found")
	}
	target.mu.Lock()
	outbox := append([]runtime.ProgressEvent(nil), target.outbox...)
	target.mu.Unlock()
	if len(outbox) != 1 || outbox[0].Type != "progress" {
		t.Fatalf("outbox %+v, want one progress event", outbox)
	}
	if _, err := f.rt.Describe(tenantCtx("acme"), ref); err != nil {
		t.Fatal(err)
	}
}

func TestFlowHostPanicFailsRunWithoutKillingProcess(t *testing.T) {
	const leak = "panic-value-must-not-leak"
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	f.flow("panic", func(_ runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		panic(leak)
	})
	f.start(t, "acme", "r1", "panic", `{}`)
	f.waitState(t, "acme", "r1", stateFailed)
	target := f.rt.lookup(core.TenantID("acme"), "r1")
	if target == nil {
		t.Fatal("run not found")
	}
	target.mu.Lock()
	runErr := target.err
	target.mu.Unlock()
	if codeOf(runErr) != "internal" {
		t.Fatalf("run error code %q, want internal", codeOf(runErr))
	}
	var platform *core.Error
	if !errors.As(runErr, &platform) {
		t.Fatalf("run error %v is not a *core.Error", runErr)
	}
	if platform.Message != "local: the flow host panicked" {
		t.Fatalf("message %q, want fixed host panic message", platform.Message)
	}
	if strings.Contains(platform.Message, leak) {
		t.Fatal("panic value leaked into the platform message")
	}
	status, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "r1"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if status.State != stateFailed {
		t.Fatalf("Describe state %q, want failed", status.State)
	}
	f.close(t)
}

type clockNowOnly struct{}

func (clockNowOnly) Now() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

