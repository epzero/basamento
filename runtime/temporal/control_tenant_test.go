package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func controlRuntimeWithDial(t *testing.T) (*Runtime, *fakeClient, *atomic.Int32) {
	t.Helper()
	fc := &fakeClient{
		listResp:     &workflowservice.ListWorkflowExecutionsResponse{},
		updateHandle: fakeUpdateHandle{},
	}
	var dials atomic.Int32
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	rt.dial = func(opts client.Options) (client.Client, error) {
		dials.Add(1)
		fc.recordDial(opts)
		return fc, nil
	}
	rt.newRunID = func() (string, error) { return "run_fixed", nil }
	return rt, fc, &dials
}

func TestControlTenantRejectsBeforeDial(t *testing.T) {
	rt, fc, dials := controlRuntimeWithDial(t)
	plain := context.Background()
	acme := core.WithTenant(context.Background(), "acme")

	type call struct {
		name string
		fn   func() error
	}
	calls := []call{
		{"start-no-tenant", func() error {
			_, e := rt.Start(plain, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}})
			return e
		}},
		{"describe-no-tenant", func() error {
			_, e := rt.Describe(plain, runtime.RunRef{RunID: "r"})
			return e
		}},
		{"cancel-no-tenant", func() error {
			return rt.Cancel(plain, runtime.RunRef{RunID: "r"}, "x")
		}},
		{"resolve-no-tenant", func() error {
			return rt.Resolve(plain, runtime.RunRef{RunID: "r"}, "g", nil)
		}},
		{"list-no-tenant", func() error {
			_, _, e := rt.List(plain, runtime.RunQuery{})
			return e
		}},
		{"list-mismatch", func() error {
			_, _, e := rt.List(acme, runtime.RunQuery{TenantID: "other"})
			return e
		}},
		{"describe-mismatch", func() error {
			_, e := rt.Describe(acme, runtime.RunRef{RunID: "r", TenantID: "other"})
			return e
		}},
		{"start-mismatch", func() error {
			_, e := rt.Start(acme, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, TenantID: "other"})
			return e
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("err %v", err)
			}
			if len(fc.Calls()) != 0 {
				t.Fatalf("client called on %s", tc.name)
			}
		})
	}
	if dials.Load() != 0 {
		t.Fatalf("dial count %d", dials.Load())
	}
}

func TestControlTenantHostileQuotes(t *testing.T) {
	hostile := []core.TenantID{
		`x" OR WorkflowId STARTS_WITH "`,
		`a"b`,
		`a'b`,
		"a`b",
		"a\u201cb",
		"a\u201db",
		"a\u2018b",
	}
	const msg = "temporal: this tenant id is not supported by the Temporal runtime"
	for _, id := range hostile {
		t.Run(string(id), func(t *testing.T) {
			if _, ok := core.TenantFrom(core.WithTenant(context.Background(), id)); !ok {
				t.Fatalf("core should accept %q for this test", id)
			}
			rt, fc, dials := controlRuntimeWithDial(t)
			ctx := core.WithTenant(context.Background(), id)
			tenantStr := string(id)
			type probe struct {
				name string
				fn   func() error
			}
			probes := []probe{
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
			for _, p := range probes {
				t.Run(p.name, func(t *testing.T) {
					err := p.fn()
					if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
						t.Fatalf("err %v", err)
					}
					var pe *core.Error
					if !errors.As(err, &pe) || pe.Message != msg {
						t.Fatalf("message %v", err)
					}
					if strings.Contains(err.Error(), tenantStr) {
						t.Fatal("echoed tenant")
					}
					if len(fc.Calls()) != 0 {
						t.Fatal("client called")
					}
					if dials.Load() != 0 {
						t.Fatal("dialed")
					}
				})
			}
			if caps := rt.Capabilities(); !caps.Durable {
				t.Fatal("capabilities should not check tenant")
			}
		})
	}
}

func TestControlTenantAllowedListQueries(t *testing.T) {
	long128 := core.TenantID(strings.Repeat("x", 126) + "yz")
	allowed := []core.TenantID{
		"acme",
		"a%b_c",
		"a b",
		"ünï",
		"a.b",
		long128,
	}
	for _, id := range allowed {
		if _, ok := core.TenantFrom(core.WithTenant(context.Background(), id)); !ok {
			t.Fatalf("core rejected %q", id)
		}
		q, err := listQuery(id, "", "")
		if err != nil {
			t.Fatalf("%q: %v", id, err)
		}
		want := `WorkflowId STARTS_WITH "` + string(id) + `/" AND ExecutionStatus != "ContinuedAsNew"`
		if q != want {
			t.Fatalf("%q got %q", id, q)
		}
	}
}

func TestControlTenantAcceptedCalls(t *testing.T) {
	rt, fc, _ := controlRuntimeWithDial(t)
	ctx := core.WithTenant(context.Background(), "acme")

	ref, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "r1"})
	if err != nil || ref.TenantID != "acme" {
		t.Fatalf("start empty field %+v err %v", ref, err)
	}
	ref2, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "r2", TenantID: "acme"})
	if err != nil || ref2.TenantID != "acme" {
		t.Fatalf("start matching field %+v err %v", ref2, err)
	}

	if _, _, err := rt.List(ctx, runtime.RunQuery{}); err != nil {
		t.Fatalf("list empty field %v", err)
	}
	if _, _, err := rt.List(ctx, runtime.RunQuery{TenantID: "acme"}); err != nil {
		t.Fatalf("list matching field %v", err)
	}

	start := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	fc.describeResp = describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, start, start)
	st, err := rt.Describe(ctx, runtime.RunRef{RunID: "r1"})
	if err != nil || st.Ref.TenantID != "acme" {
		t.Fatalf("describe %+v err %v", st.Ref, err)
	}
	st2, err := rt.Describe(ctx, runtime.RunRef{RunID: "r1", TenantID: "acme"})
	if err != nil || st2.Ref.TenantID != "acme" {
		t.Fatalf("describe matching %+v err %v", st2.Ref, err)
	}

	if err := rt.Resolve(ctx, runtime.RunRef{RunID: "r1"}, "g", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("resolve %v", err)
	}
	if err := rt.Cancel(ctx, runtime.RunRef{RunID: "r1"}, "why"); err != nil {
		t.Fatalf("cancel %v", err)
	}
}
