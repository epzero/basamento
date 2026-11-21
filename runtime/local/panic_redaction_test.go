package local

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

const secretToken = "s3cr3t-token"

func TestStepPanicRedactsCauseFromCaller(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var stepErr error
	if err := f.rt.RegisterStep("leak", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic(secretToken)
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		stepErr = dc.Step("leak", nil, runtime.StepOptions{}).Get(nil)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "parent", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	if stepErr == nil {
		t.Fatal("want step error")
	}
	if strings.Contains(stepErr.Error(), secretToken) {
		t.Fatalf("caller-facing error leaks token: %v", stepErr)
	}
	if errors.Unwrap(stepErr) != nil {
		t.Fatalf("unwrap %v, want nil", errors.Unwrap(stepErr))
	}
	if core.CodeOf(stepErr) != core.CodeInternal {
		t.Fatalf("code %v, want internal", core.CodeOf(stepErr))
	}
	var plat *core.Error
	if !errors.As(stepErr, &plat) || plat.Details["step"] != "leak" {
		t.Fatalf("details %v, want step leak", plat)
	}
}

func TestFlowPanicRedactsCauseFromParentChildGet(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("panic-flow", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		panic(secretToken)
	})
	var parentErr error
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		parentErr = dc.Child(runtime.FlowRef{Name: "panic-flow"}, nil, runtime.ChildOptions{ID: "c1"}).Get(nil)
		return nil, parentErr
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	f.waitState(t, "acme", "p1", stateFailed)
	f.waitState(t, "acme", "c1", stateFailed)
	if parentErr == nil {
		t.Fatal("want child future error")
	}
	if strings.Contains(parentErr.Error(), secretToken) {
		t.Fatalf("parent Get leaks token: %v", parentErr)
	}
}