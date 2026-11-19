package conformance_test

import (
	"context"
	"flag"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform/secrets"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/conformance"
	"github.com/epzero/basamento/runtime/local"
	"github.com/epzero/basamento/runtime/temporal"
)

// update rewrites the golden records unless the test is run without it. Golden
// files change only with this flag and a reason in the commit body.
var update = flag.Bool("update", false, "rewrite the conformance golden records")

// plainCodec is the test's payload codec: the conformance flows observe decoded
// values only, so this test keeps the codec out of the way.
type plainCodec struct{}

func (plainCodec) Encode(_ context.Context, p secrets.Payload) (temporal.Encoded, error) {
	return temporal.Encoded{Payload: p}, nil
}

func (plainCodec) Decode(_ context.Context, p secrets.Payload) (secrets.Payload, error) {
	return p, nil
}

// TestContinueAsNewOnTemporalRuntime runs the continue-as-new flow with a
// checkpoint threshold below its length: the run must continue as new at least
// once and still match the golden record the local runtime produced.
func TestContinueAsNewOnTemporalRuntime(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rt, err := temporal.New(temporal.Options{
		Clock:               clock,
		IDs:                 &coretest.SeqIds{},
		Codec:               plainCodec{},
		CheckpointThreshold: 20,
	})
	if err != nil {
		t.Fatalf("temporal: %v", err)
	}
	h := conformance.New("continue-as-new", clock)
	if err := h.Register(rt); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	rec, err := h.Run(ctx, rt, "r1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	count, err := rt.ContinueAsNewCount(ctx, runtime.RunRef{RunID: "r1", TenantID: "acme"})
	if err != nil {
		t.Fatalf("continue-as-new count: %v", err)
	}
	if count < 1 {
		t.Fatalf("continued as new %d times, want at least 1", count)
	}
	if err := conformance.MatchGolden(filepath.Join("testdata", "continue-as-new.golden.json"), rec, *update); err != nil {
		t.Fatal(err)
	}
}

func TestGoldenFlowsOnLocalRuntime(t *testing.T) {
	names := make([]string, 0, len(conformance.Specs))
	for name := range conformance.Specs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			clock := coretest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			rt := local.New(local.Options{Clock: clock, IDs: &coretest.SeqIds{}})
			h := conformance.New(name, clock)
			if err := h.Register(rt); err != nil {
				t.Fatalf("register: %v", err)
			}
			ctx := core.WithTenant(context.Background(), "acme")
			rec, err := h.Run(ctx, rt, "run-"+name)
			if err != nil {
				t.Fatalf("run %s: %v", name, err)
			}
			if rec.State != "completed" {
				t.Fatalf("state %q, want completed", rec.State)
			}
			if len(rec.Events) == 0 {
				t.Fatal("record holds no durable events")
			}
			if len(rec.Steps) == 0 {
				t.Fatal("record holds no steps")
			}
			path := filepath.Join("testdata", name+".golden.json")
			if err := conformance.MatchGolden(path, rec, *update); err != nil {
				t.Fatal(err)
			}
		})
	}
}
