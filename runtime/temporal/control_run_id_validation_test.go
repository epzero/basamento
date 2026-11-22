package temporal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

const invalidRunIDMsg = "temporal: invalid run id"

var slashRunIDs = []struct {
	name  string
	runID string
}{
	{"mid", "a/b"},
	{"leading", "/x"},
	{"trailing", "x/"},
	{"tenant_like", "acme/r1"},
}

func TestStartRejectsInvalidGeneratedRunIDBeforeDial(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	rt, fc := runtimeNoDial(t)
	rt.newRunID = func() (string, error) { return "a/b", nil }
	_, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}})
	assertInvalidRunID(t, err, "a/b")
	if fc.clientOps() != 0 || fc.dialCount() != 0 {
		t.Fatalf("expected no client activity: dials=%d ops=%d", fc.dialCount(), fc.clientOps())
	}
}

func TestStartRejectsSlashRunIDBeforeDial(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	for _, row := range slashRunIDs {
		t.Run(row.name, func(t *testing.T) {
			rt, fc := runtimeNoDial(t)
			_, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: row.runID})
			assertInvalidRunID(t, err, row.runID)
			if fc.clientOps() != 0 || fc.dialCount() != 0 {
				t.Fatalf("expected no client activity: dials=%d ops=%d", fc.dialCount(), fc.clientOps())
			}
		})
	}
}

func TestDescribeCancelResolveRejectSlashRunIDBeforeDial(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	for _, row := range slashRunIDs {
		t.Run(row.runID, func(t *testing.T) {
			probes := []struct {
				name string
				call func(*Runtime) error
			}{
				{"describe", func(rt *Runtime) error {
					_, err := rt.Describe(ctx, runtime.RunRef{RunID: row.runID})
					return err
				}},
				{"cancel", func(rt *Runtime) error {
					return rt.Cancel(ctx, runtime.RunRef{RunID: row.runID}, "why")
				}},
				{"resolve", func(rt *Runtime) error {
					return rt.Resolve(ctx, runtime.RunRef{RunID: row.runID}, "g", nil)
				}},
			}
			for _, probe := range probes {
				t.Run(probe.name, func(t *testing.T) {
					rt, fc := runtimeNoDial(t)
					err := probe.call(rt)
					assertInvalidRunID(t, err, row.runID)
					if fc.clientOps() != 0 || fc.dialCount() != 0 {
						t.Fatalf("expected no client activity: dials=%d ops=%d", fc.dialCount(), fc.clientOps())
					}
				})
			}
		})
	}
}

func assertInvalidRunID(t *testing.T, err error, runID string) {
	t.Helper()
	var pe *core.Error
	if err == nil || !errors.As(err, &pe) || pe.Code != core.CodeInvalidArgument || pe.Message != invalidRunIDMsg {
		t.Fatalf("expected invalid_argument run id, got %v", err)
	}
	if strings.Contains(err.Error(), runID) {
		t.Fatal("error echoed run id")
	}
}
