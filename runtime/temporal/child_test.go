package temporal

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/converter"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func registerSubflowHandlers(t *testing.T, rt *Runtime, flows map[string]func(runtime.DurableContext) (json.RawMessage, error)) {
	t.Helper()
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, ref runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		fn, ok := flows[ref.Name]
		if !ok {
			return nil, core.NewError(core.CodeNotFound, "flow", nil)
		}
		return fn(dc)
	})
}

func parentCloseFlowFixtures(policy string) map[string]func(runtime.DurableContext) (json.RawMessage, error) {
	return map[string]func(runtime.DurableContext) (json.RawMessage, error){
		"parent": func(dc runtime.DurableContext) (json.RawMessage, error) {
			_ = dc.Child(runtime.FlowRef{Name: "child-long"}, nil, runtime.ChildOptions{
				ParentClose: policy,
				ID:          "c1",
			})
			if err := dc.Sleep(time.Second).Get(nil); err != nil {
				return nil, err
			}
			return json.RawMessage(`"parent-done"`), nil
		},
		"child-long": func(dc runtime.DurableContext) (json.RawMessage, error) {
			if err := dc.Sleep(time.Hour).Get(nil); err != nil {
				return nil, err
			}
			return json.RawMessage(`"child-done"`), nil
		},
	}
}

func TestChildResultAndFailureCodeAndRetryable(t *testing.T) {
	rt, env := testEnv(t)
	type row struct {
		Code      string `json:"code,omitempty"`
		Message   string `json:"message,omitempty"`
		Retryable bool   `json:"retryable,omitempty"`
		Result    string `json:"result,omitempty"`
	}
	registerSubflowHandlers(t, rt, map[string]func(runtime.DurableContext) (json.RawMessage, error){
		"parent": func(dc runtime.DurableContext) (json.RawMessage, error) {
			specs := []struct {
				flow string
				id   string
			}{
				{"child-ok", "c1"},
				{"child-retry", "c2"},
				{"child-fail", "c3"},
			}
			var rows []row
			for _, spec := range specs {
				var raw json.RawMessage
				err := dc.Child(runtime.FlowRef{Name: spec.flow}, nil, runtime.ChildOptions{ID: spec.id}).Get(&raw)
				var r row
				if err != nil {
					var pe *core.Error
					if !errors.As(err, &pe) {
						return nil, err
					}
					r = row{Code: string(pe.Code), Message: pe.Message, Retryable: pe.Retryable}
				} else {
					if err := json.Unmarshal(raw, &r.Result); err != nil {
						return nil, err
					}
				}
				rows = append(rows, r)
			}
			return json.Marshal(rows)
		},
		"child-ok": func(runtime.DurableContext) (json.RawMessage, error) {
			return json.RawMessage(`"child-ok"`), nil
		},
		"child-retry": func(runtime.DurableContext) (json.RawMessage, error) {
			return nil, &core.Error{Code: core.CodeUnavailable, Message: "temporal: child down", Retryable: true}
		},
		"child-fail": func(runtime.DurableContext) (json.RawMessage, error) {
			return nil, core.NewError(core.CodeInvalidArgument, "temporal: bad", nil)
		},
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "parent"}, RunID: "r1"})
	var got []row
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatal(err)
	}
	want := []row{
		{Result: "child-ok"},
		{Code: string(core.CodeUnavailable), Message: "temporal: child down", Retryable: true},
		{Code: string(core.CodeInvalidArgument), Message: "temporal: bad"},
	}
	if len(got) != len(want) {
		t.Fatalf("len %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestChildParentClosePolicyTerminateIsDefault(t *testing.T) {
	runParentClosePolicyTest(t, "", func(childErr error) {
		var term *sdktemporal.TerminatedError
		if !errors.As(childErr, &term) {
			t.Fatalf("child err %v", childErr)
		}
	})
}

func TestChildParentClosePolicyRequestCancel(t *testing.T) {
	runParentClosePolicyTest(t, "request_cancel", func(childErr error) {
		if childErr == nil {
			t.Fatal("expected child cancelled")
		}
		var app *sdktemporal.ApplicationError
		if !errors.As(childErr, &app) {
			t.Fatalf("child err %T %v", childErr, childErr)
		}
		if app.Type() != string(core.CodeCancelled) || app.Message() != "temporal: activity cancelled" {
			t.Fatalf("child err type=%q msg=%q", app.Type(), app.Message())
		}
	})
}

func TestChildParentClosePolicyAbandon(t *testing.T) {
	runParentClosePolicyTest(t, "abandon", func(childErr error) {
		if childErr != nil {
			t.Fatalf("child err %v", childErr)
		}
	})
}

func runParentClosePolicyTest(t *testing.T, policy string, assertChild func(error)) {
	t.Helper()
	rt, env := testEnv(t)
	const childWfID = "acme/c1"
	var starts int
	env.SetOnChildWorkflowStartedListener(func(wi *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
		if wi.WorkflowExecution.ID == childWfID {
			starts++
		}
	})
	registerSubflowHandlers(t, rt, parentCloseFlowFixtures(policy))
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "parent"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("parent: %v", err)
	}
	if starts != 1 {
		t.Fatalf("starts %d", starts)
	}
	assertChild(env.GetWorkflowErrorByID(childWfID))
}

func TestChildInvalidParentCloseIsInvalidArgument(t *testing.T) {
	rt, env := testEnv(t)
	var starts int
	type childErrorJSON struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	var out childErrorJSON
	env.SetOnChildWorkflowStartedListener(func(*workflow.Info, workflow.Context, converter.EncodedValues) {
		starts++
	})
	registerSubflowHandlers(t, rt, map[string]func(runtime.DurableContext) (json.RawMessage, error){
		"parent": func(dc runtime.DurableContext) (json.RawMessage, error) {
			err := dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{ParentClose: "bad", ID: "c1"}).Get(nil)
			if err != nil {
				var pe *core.Error
				if errors.As(err, &pe) {
					out = childErrorJSON{Code: string(pe.Code), Message: pe.Message}
				} else {
					return nil, err
				}
			}
			return json.Marshal(out)
		},
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "parent"}, RunID: "r1"})
	if starts != 0 {
		t.Fatalf("starts %d", starts)
	}
	if out.Code != string(core.CodeInvalidArgument) || out.Message != `temporal: unknown parent close policy "bad"` {
		t.Fatalf("out %+v", out)
	}
}

func TestChildSameIDTwiceReturnsSameFuture(t *testing.T) {
	rt, env := testEnv(t)
	var starts int
	var same bool
	var r1, r2 json.RawMessage
	env.SetOnChildWorkflowStartedListener(func(*workflow.Info, workflow.Context, converter.EncodedValues) {
		starts++
	})
	registerSubflowHandlers(t, rt, map[string]func(runtime.DurableContext) (json.RawMessage, error){
		"parent": func(dc runtime.DurableContext) (json.RawMessage, error) {
			f1 := dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{ID: "same"})
			f2 := dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{ID: "same"})
			same = f1 == f2
			if err := f1.Get(&r1); err != nil {
				return nil, err
			}
			if err := f2.Get(&r2); err != nil {
				return nil, err
			}
			return json.Marshal(same)
		},
		"child-flow": func(runtime.DurableContext) (json.RawMessage, error) {
			return json.RawMessage(`"x"`), nil
		},
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "parent"}, RunID: "r1"})
	if !same || starts != 1 || string(r1) != string(r2) {
		t.Fatalf("same=%v starts=%d r1=%s r2=%s", same, starts, r1, r2)
	}
}

func TestChildDefaultIDUsesNewIDAndIsStableAcrossReplay(t *testing.T) {
	rt := chainRuntime(t, 10)
	var wfIDs []string
	registerSubflowHandlers(t, rt, map[string]func(runtime.DurableContext) (json.RawMessage, error){
		"parent": func(dc runtime.DurableContext) (json.RawMessage, error) {
			var r1, r2 json.RawMessage
			if err := dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{}).Get(&r1); err != nil {
				return nil, err
			}
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
			if err := dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{}).Get(&r2); err != nil {
				return nil, err
			}
			return json.Marshal([]json.RawMessage{r1, r2})
		},
		"child-flow": func(runtime.DurableContext) (json.RawMessage, error) {
			return json.RawMessage(`"ok"`), nil
		},
	})
	opts := defaultChainOpts()
	hook := func(_ int, env *testsuite.TestWorkflowEnvironment) {
		env.SetOnChildWorkflowStartedListener(func(wi *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
			wfIDs = append(wfIDs, wi.WorkflowExecution.ID)
		})
	}
	opts.perExec = make([]func(int, *testsuite.TestWorkflowEnvironment), opts.maxExec)
	for i := range opts.perExec {
		opts.perExec[i] = hook
	}
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "parent"}, RunID: "r1"}, opts)
	if res.Execs != 2 {
		t.Fatalf("execs %d", res.Execs)
	}
	want := []string{"acme/child_1", "acme/child_2"}
	if len(wfIDs) != 2 || wfIDs[0] != want[0] || wfIDs[1] != want[1] {
		t.Fatalf("wfIDs %v", wfIDs)
	}
}

func TestChildInheritsTenant(t *testing.T) {
	rt, env := testEnv(t)
	var tenant core.TenantID
	var ok bool
	registerSubflowHandlers(t, rt, map[string]func(runtime.DurableContext) (json.RawMessage, error){
		"parent": func(dc runtime.DurableContext) (json.RawMessage, error) {
			return nil, dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{}).Get(nil)
		},
		"child-flow": func(dc runtime.DurableContext) (json.RawMessage, error) {
			tenant, ok = core.TenantFrom(dc.Context())
			return json.RawMessage(`"c"`), nil
		},
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "parent"}, RunID: "r1"})
	if tenant != core.TenantID("acme") || !ok {
		t.Fatalf("tenant %q ok %v", tenant, ok)
	}
}

func TestChildWorkflowIDIsTenantScoped(t *testing.T) {
	rt, env := testEnv(t)
	var wfID string
	env.SetOnChildWorkflowStartedListener(func(wi *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
		wfID = wi.WorkflowExecution.ID
	})
	registerSubflowHandlers(t, rt, map[string]func(runtime.DurableContext) (json.RawMessage, error){
		"parent": func(dc runtime.DurableContext) (json.RawMessage, error) {
			return nil, dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{ID: "cid"}).Get(nil)
		},
		"child-flow": func(runtime.DurableContext) (json.RawMessage, error) {
			return json.RawMessage(`"c"`), nil
		},
	})
	registerAcmeEnv(t, rt, env)
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "parent"}, RunID: "r1"})
	if wfID != "acme/cid" {
		t.Fatalf("wf %q", wfID)
	}
}

func TestChildJournaledOutcomeIsNotStartedAgain(t *testing.T) {
	var starts atomic.Int32
	rt := chainRuntime(t, 10)
	registerSubflowHandlers(t, rt, map[string]func(runtime.DurableContext) (json.RawMessage, error){
		"parent": func(dc runtime.DurableContext) (json.RawMessage, error) {
			var r1, r2 json.RawMessage
			if err := dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{ID: "c1"}).Get(&r1); err != nil {
				return nil, err
			}
			if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
				return nil, err
			}
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
			if err := dc.Child(runtime.FlowRef{Name: "child-flow"}, nil, runtime.ChildOptions{ID: "c1"}).Get(&r2); err != nil {
				return nil, err
			}
			return json.Marshal([]json.RawMessage{r1, r2})
		},
		"child-flow": func(runtime.DurableContext) (json.RawMessage, error) {
			starts.Add(1)
			return json.RawMessage(`"c"`), nil
		},
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "parent"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 2 || starts.Load() != 1 {
		t.Fatalf("execs %d starts %d", res.Execs, starts.Load())
	}
}

func TestWorkflowIDHelper(t *testing.T) {
	if got := workflowID("", "run-1"); got != "run-1" {
		t.Fatalf("empty tenant: got %q want run-1", got)
	}
	if got := workflowID("acme", "run-1"); got != "acme/run-1" {
		t.Fatalf("tenant-scoped: got %q want acme/run-1", got)
	}
}
