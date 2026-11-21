package temporal

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
)

func TestDataConverterWrappedWithoutDeadlock(t *testing.T) {
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	dc := rt.DataConverter()
	// SDK v1.47 pins the wrapper type name; this is the observable seam.
	typeName := fmt.Sprintf("%T", dc)
	if !strings.Contains(typeName, "dataConverterWithoutDeadlock") {
		t.Fatalf("converter type %q", typeName)
	}
	if strings.HasSuffix(typeName, "temporal.dataConverter") {
		t.Fatalf("unwrapped converter %q", typeName)
	}
	aware, ok := dc.(interface {
		WithContext(context.Context) converter.DataConverter
		WithWorkflowContext(workflow.Context) converter.DataConverter
	})
	if !ok {
		t.Fatal("wrapper must stay context-aware")
	}
	ctx := core.WithTenant(context.Background(), "acme")
	wctx := aware.WithContext(ctx)
	p, err := wctx.ToPayload("probe-value")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || len(p.Data) == 0 {
		t.Fatal("empty payload")
	}
	var back string
	if err := wctx.FromPayload(p, &back); err != nil {
		t.Fatal(err)
	}
	if back != "probe-value" {
		t.Fatalf("got %q", back)
	}
	// WithWorkflowContext must be nil-safe on the client side (SDK contract).
	if aware.WithWorkflowContext(nil) == nil {
		t.Fatal("nil workflow context converter")
	}
}

func TestCodecFailureLogRecord(t *testing.T) {
	var buf bytes.Buffer
	rt := &Runtime{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))}
	rt.logCodecFailure("acme", "wf-1", "big", core.NewError(
		core.CodeUnavailable, "INJECTED-SECRET-CAUSE", map[string]any{"k": "v"}))
	got := buf.String()
	if !strings.Contains(got, "unavailable") || !strings.Contains(got, "INJECTED-SECRET-CAUSE") {
		t.Fatalf("log %q", got)
	}
	if !strings.Contains(got, "workflow=wf-1") {
		t.Fatalf("log %q", got)
	}
}
