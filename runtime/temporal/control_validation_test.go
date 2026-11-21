package temporal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.temporal.io/sdk/client"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

type badInput struct {
	name string
	text string
}

var flowBadInputs = []badInput{
	{"double-quote", `a"b`},
	{"single-quote", `a'b`},
	{"backtick", "a`b"},
	{"backslash", `a\b`},
	{"unicode-left-double", "a\u201cb"},
	{"unicode-right-double", "a\u201db"},
	{"unicode-left-single", "a\u2018b"},
	{"unicode-right-single", "a\u2019b"},
	{"unicode-double-angle", "a\u00ab b\u00bb"},
	{"newline", "a\nb"},
	{"tab", "a\tb"},
	{"nul", "a\x00b"},
	{"del", "a\x7fb"},
	{"invalid-utf8", string([]byte{'a', 0xff, 'b'})},
	{"long-256", strings.Repeat("f", 256)},
}

var tenantQuoteInputs = []badInput{
	{"double-quote", `a"b`},
	{"single-quote", `a'b`},
	{"backtick", "a`b"},
	{"unicode-left-double", "a\u201cb"},
	{"unicode-right-double", "a\u201db"},
	{"unicode-left-single", "a\u2018b"},
	{"unicode-right-single", "a\u2019b"},
	{"unicode-double-angle", "a\u00ab"},
}

var tenantNoTenantInputs = []badInput{
	{"backslash", `a\b`},
	{"newline", "a\nb"},
	{"nul", "a\x00b"},
	{"invalid-utf8", string([]byte{'a', 0xff})},
}

const (
	invalidFlowMsg    = "temporal: invalid flow filter"
	unsupportedTenMsg = "temporal: this tenant id is not supported by the Temporal runtime"
	noTenantMsg       = "temporal: no tenant in the context"
)

func TestFlowFilterRejectsEachUnsafeClass(t *testing.T) {
	tenant := core.TenantID("acme")
	ctx := core.WithTenant(context.Background(), "acme")
	for _, bad := range flowBadInputs {
		t.Run(bad.name, func(t *testing.T) {
			t.Run("listQuery", func(t *testing.T) {
				_, err := listQuery(tenant, bad.text, "")
				assertFlowRejected(t, err, bad.text)
			})
			t.Run("List", func(t *testing.T) {
				rt, fc := runtimeNoDial(t)
				_, _, err := rt.List(ctx, runtime.RunQuery{Flow: bad.text})
				assertFlowRejected(t, err, bad.text)
				if fc.clientOps() != 0 || fc.dialCount() != 0 {
					t.Fatalf("expected no client ops: dials=%d ops=%d", fc.dialCount(), fc.clientOps())
				}
			})
		})
	}
}

func TestTenantRejectsEachQuoteClass(t *testing.T) {
	for _, bad := range tenantQuoteInputs {
		t.Run(bad.name, func(t *testing.T) {
			if _, ok := core.TenantFrom(core.WithTenant(context.Background(), core.TenantID(bad.text))); !ok {
				t.Fatalf("core should accept %q for quote test", bad.text)
			}
			rt, fc := runtimeNoDial(t)
			ctx := core.WithTenant(context.Background(), core.TenantID(bad.text))
			for _, fn := range tenantProbes(rt, ctx) {
				t.Run(fn.name, func(t *testing.T) {
					err := fn.call()
					assertTenantUnsupported(t, err, bad.text)
					if fc.clientOps() != 0 || fc.dialCount() != 0 {
						t.Fatalf("client activity: dials=%d ops=%d", fc.dialCount(), fc.clientOps())
					}
				})
			}
		})
	}
}

func TestTenantCoreRejectedNoTenantInContext(t *testing.T) {
	for _, bad := range tenantNoTenantInputs {
		t.Run(bad.name, func(t *testing.T) {
			ctx := core.WithTenant(context.Background(), core.TenantID(bad.text))
			if _, ok := core.TenantFrom(ctx); ok {
				t.Fatalf("core should reject %q", bad.text)
			}
			rt, fc := runtimeNoDial(t)
			for _, fn := range tenantProbes(rt, ctx) {
				t.Run(fn.name, func(t *testing.T) {
					err := fn.call()
					assertNoTenant(t, err)
					if fc.clientOps() != 0 || fc.dialCount() != 0 {
						t.Fatalf("client activity: dials=%d ops=%d", fc.dialCount(), fc.clientOps())
					}
				})
			}
		})
	}
}

func assertFlowRejected(t *testing.T, err error, text string) {
	t.Helper()
	var pe *core.Error
	if err == nil || !errors.As(err, &pe) || pe.Code != core.CodeInvalidArgument || pe.Message != invalidFlowMsg {
		t.Fatalf("expected invalid_argument flow filter, got %v", err)
	}
	if strings.Contains(err.Error(), text) {
		t.Fatal("error echoed flow text")
	}
}

func assertTenantUnsupported(t *testing.T, err error, text string) {
	t.Helper()
	var pe *core.Error
	if err == nil || !errors.As(err, &pe) || pe.Code != core.CodeInvalidArgument || pe.Message != unsupportedTenMsg {
		t.Fatalf("expected unsupported tenant, got %v", err)
	}
	if strings.Contains(err.Error(), text) {
		t.Fatal("error echoed tenant text")
	}
}

func assertNoTenant(t *testing.T, err error) {
	t.Helper()
	var pe *core.Error
	if err == nil || !errors.As(err, &pe) || pe.Code != core.CodeInvalidArgument || pe.Message != noTenantMsg {
		t.Fatalf("expected no tenant, got %v", err)
	}
}

type tenantProbe struct {
	name string
	call func() error
}

func tenantProbes(rt *Runtime, ctx context.Context) []tenantProbe {
	return []tenantProbe{
		{"start", func() error {
			_, e := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}})
			return e
		}},
		{"describe", func() error {
			_, e := rt.Describe(ctx, runtime.RunRef{RunID: "r"})
			return e
		}},
		{"cancel", func() error {
			return rt.Cancel(ctx, runtime.RunRef{RunID: "r"}, "x")
		}},
		{"resolve", func() error {
			return rt.Resolve(ctx, runtime.RunRef{RunID: "r"}, "g", nil)
		}},
		{"list", func() error {
			_, _, e := rt.List(ctx, runtime.RunQuery{})
			return e
		}},
	}
}

func runtimeNoDial(t *testing.T) (*Runtime, *fakeClient) {
	t.Helper()
	fc := &fakeClient{}
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	rt.dial = func(client.Options) (client.Client, error) {
		fc.recordDial(client.Options{})
		return fc, nil
	}
	return rt, fc
}
