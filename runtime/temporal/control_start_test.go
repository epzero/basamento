package temporal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestStartHappyPath(t *testing.T) {
	fc := &fakeClient{}
	rt := testControlRuntime(t, fc)
	ctx := core.WithTenant(context.Background(), "acme")
	in := json.RawMessage(`{"x":1}`)
	ref, err := rt.Start(ctx, runtime.StartRequest{
		Flow:  runtime.FlowRef{Name: "pay", Hash: "h"},
		Input: in,
		RunID: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref != (runtime.RunRef{RunID: "r1", TenantID: "acme"}) {
		t.Fatalf("ref %#v", ref)
	}
	opts := mustLastCall(fc, "execute").ExecuteOpts
	if opts.ID != "acme/r1" || opts.TaskQueue != DefaultTaskQueue {
		t.Fatalf("opts %#v", opts)
	}
	if opts.WorkflowIDReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE ||
		opts.WorkflowIDConflictPolicy != enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL ||
		!opts.WorkflowExecutionErrorWhenAlreadyStarted {
		t.Fatalf("policies %#v", opts)
	}
	flowName, ok := opts.TypedSearchAttributes.GetKeyword(flowSearchAttributeKey)
	if !ok || flowName != "pay" {
		t.Fatalf("search attr %q ok=%v", flowName, ok)
	}
	hi := mustLastCall(fc, "execute").ExecuteArgs[0].(HostInput)
	if hi.RunID != "r1" || hi.Flow.Name != "pay" || hi.Flow.Hash != "h" || string(hi.Input) != string(in) {
		t.Fatalf("host input %#v", hi)
	}
}

func TestDeriveRunIDIncludesTenant(t *testing.T) {
	want := deriveRunID("acme", "k1")
	sum := sha256.Sum256([]byte("acme\x00k1"))
	if want != "idem_"+hex.EncodeToString(sum[:]) {
		t.Fatalf("derive id: got %q want idem_%s", want, hex.EncodeToString(sum[:]))
	}
	if deriveRunID("beta", "k1") == want {
		t.Fatalf("derive id must differ across tenants")
	}
}

func TestStartIdempotentDuplicateSuccess(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	want := deriveRunID("acme", "k1")
	fc := &fakeClient{executeErr: &serviceerror.WorkflowExecutionAlreadyStarted{}}
	rt := testControlRuntime(t, fc)
	ref, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, IdempotencyKey: "k1"})
	if err != nil || ref.RunID != want {
		t.Fatalf("expected success ref run_id=%s, got ref=%+v err=%v", want, ref, err)
	}
}

func TestStartExplicitRunIDConflict(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	fc := &fakeClient{executeErr: &serviceerror.WorkflowExecutionAlreadyStarted{}}
	rt := testControlRuntime(t, fc)
	_, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "r1"})
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Code != core.CodeConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	if pe.Details == nil || pe.Details["run"] != "r1" {
		t.Fatalf("conflict details: got %#v want run=r1", pe.Details)
	}
}

func TestStartIdempotentDuplicateHasNoConflictDetails(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	fc := &fakeClient{executeErr: &serviceerror.WorkflowExecutionAlreadyStarted{}}
	rt := testControlRuntime(t, fc)
	ref, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("expected success for derived duplicate, got %v", err)
	}
	if ref.RunID == "" {
		t.Fatal("empty ref")
	}
}

func TestStartRejectsKeyAndRunIDTogether(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	rt := testControlRuntime(t, &fakeClient{})
	_, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "r1", IdempotencyKey: "k"})
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("expected invalid_argument for key+run, got %v", err)
	}
}

func TestStartRunIDGenerationAndValidation(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	var gen atomic.Int32
	fc := &fakeClient{}
	rt := testControlRuntime(t, fc)
	rt.newRunID = func() (string, error) {
		gen.Add(1)
		return "run_fixed", nil
	}
	if _, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "explicit"}); err != nil {
		t.Fatal(err)
	}
	if gen.Load() != 0 {
		t.Fatal("newRunID used for explicit id")
	}
	if _, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, IdempotencyKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if gen.Load() != 0 {
		t.Fatal("newRunID used for idempotency key")
	}
	ref, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}})
	if err != nil || ref.RunID != "run_fixed" || gen.Load() != 1 {
		t.Fatalf("generate %+v err %v gen %d", ref, err, gen.Load())
	}
	if _, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "a/b"}); err != nil {
		t.Fatal("slash allowed")
	}
	if _, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: ""}, RunID: "r"}); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("empty flow %v", err)
	}
	bad := []string{strings.Repeat("x", 256), "bad\x00", "bad\x7f", string([]byte{0xff})}
	for _, id := range bad {
		if _, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: id}); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("invalid %q: %v", id, err)
		}
	}
	if !utf8.ValidString("a/b") {
		t.Fatal("test setup")
	}
}

func TestStartPropagatorTenantHeader(t *testing.T) {
	fc := &fakeClient{}
	rt := testControlRuntime(t, fc)
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "r"}); err != nil {
		t.Fatal(err)
	}
	var dialOpts client.Options
	for _, c := range fc.Calls() {
		if c.Op == "dial" {
			dialOpts = c.DialOpts
		}
	}
	if len(dialOpts.ContextPropagators) != 1 {
		t.Fatalf("propagators %d", len(dialOpts.ContextPropagators))
	}
	prop := dialOpts.ContextPropagators[0]
	writer := &headerCarrier{}
	if err := prop.Inject(ctx, writer); err != nil {
		t.Fatal(err)
	}
	var tenant string
	if err := converter.GetDefaultDataConverter().FromPayload(writer.payloads[tenantHeaderKey], &tenant); err != nil {
		t.Fatal(err)
	}
	if tenant != "acme" {
		t.Fatalf("tenant %q", tenant)
	}
}

type headerCarrier struct {
	payloads map[string]*commonpb.Payload
}

func (h *headerCarrier) Set(key string, value *commonpb.Payload) {
	if h.payloads == nil {
		h.payloads = map[string]*commonpb.Payload{}
	}
	h.payloads[key] = value
}
func (h *headerCarrier) Get(key string) (*commonpb.Payload, bool) {
	p, ok := h.payloads[key]
	return p, ok
}
func (h *headerCarrier) ForEachKey(handler func(key string, value *commonpb.Payload) error) error {
	return nil
}

var _ workflow.HeaderWriter = (*headerCarrier)(nil)
var _ workflow.HeaderReader = (*headerCarrier)(nil)

func TestNewRejectsClientCodecOverrides(t *testing.T) {
	_, err := New(Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
		Outbox:   newTestOutbox(),
		Client:   client.Options{DataConverter: converter.GetDefaultDataConverter()},
	})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("dc err %v", err)
	}
	_, err = New(Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
		Outbox:   newTestOutbox(),
		Client:   client.Options{ContextPropagators: []workflow.ContextPropagator{TenantPropagator{}}},
	})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("prop err %v", err)
	}
}
