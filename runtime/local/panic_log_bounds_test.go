package local

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/epzero/basamento/runtime"
)

func TestStepPanicLogIntValue(t *testing.T) {
	log := &captureHandler{}
	f := newLoggedFixture(t, log)
	defer f.close(t)
	if err := f.rt.RegisterStep("int", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic(42)
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		_ = dc.Step("int", nil, runtime.StepOptions{}).Get(nil)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "parent", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	if got := attrString(log.last(), "panic"); got != "42" {
		t.Fatalf("panic field %q, want 42", got)
	}
}

func TestStepPanicLogBoundsHugeValue(t *testing.T) {
	log := &captureHandler{}
	f := newLoggedFixture(t, log)
	defer f.close(t)
	huge := strings.Repeat("é", 1<<20)
	if err := f.rt.RegisterStep("huge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic(huge)
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		_ = dc.Step("huge", nil, runtime.StepOptions{}).Get(nil)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "parent", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	panicField := attrString(log.last(), "panic")
	if utf8.RuneCountInString(panicField) > maxPanicValue+utf8.RuneCountInString("... (truncated)")+1 {
		t.Fatalf("panic field %d runes, want bounded", utf8.RuneCountInString(panicField))
	}
	if !strings.HasSuffix(panicField, "... (truncated)") {
		t.Fatalf("panic field missing truncation marker")
	}
	if !strings.HasPrefix(panicField, "é") {
		t.Fatal("panic field should start with panic value")
	}
}

func deepRecurse(step string, n int) {
	if n == 0 {
		panic(step)
	}
	deepRecurse(step, n-1)
}

// Go elides middle frames in debug.Stack itself for deep recursion, so the
// 16 KiB cap is covered by TestFormatPanicStackTruncates, not this scenario.
func TestStepPanicLogBoundsDeepStack(t *testing.T) {
	log := &captureHandler{}
	f := newLoggedFixture(t, log)
	defer f.close(t)
	const depth = 3000
	if err := f.rt.RegisterStep("deep", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		deepRecurse("deep", depth)
		return nil, nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		_ = dc.Step("deep", nil, runtime.StepOptions{}).Get(nil)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "parent", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	stack := attrString(log.last(), "stack")
	if len(stack) > stackLogCap+len("... (stack truncated)")+1 {
		t.Fatalf("stack len %d, want bounded", len(stack))
	}
	if !strings.Contains(stack, "deepRecurse") {
		t.Fatal("stack should contain panicking function near the top")
	}
}
