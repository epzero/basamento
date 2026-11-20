package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

func registerLocalChildIDs(sc *godog.ScenarioContext) {
	sc.Step(`^a run of "([^"]*)" for tenant "([^"]*)" has child run "([^"]*)" waiting at gate "([^"]*)"$`,
		func(ctx context.Context, parent, tenant, childID, gate string) error {
			w := localStepWorld(ctx)
			reg, ok := w.parentReg(parent)
			if !ok {
				reg = parentChildReg{child: localDefaultParentFlow, id: childID, policy: localDefaultClosePolicy}
			}
			w.setChildGateFlow(reg.child, gate)
			w.setCommandParent(parent, &reg)
			ref, exec, err := w.start(ctx, tenant, "", parent)
			if err != nil {
				return err
			}
			if err := w.waitRunState(ctx, ref, "running"); err != nil {
				return err
			}
			var state string
			err = w.spin("child "+childID+" to start", func() bool {
				var spinErr error
				state, spinErr = w.stateOf(ctx, tenant, childID)
				return spinErr == nil && state == "running"
			})
			if err != nil {
				return fmt.Errorf("child %q: %w", childID, err)
			}
			w.mu.Lock()
			w.current = ref
			w.currentExec = exec
			w.mu.Unlock()
			return w.waitRecordedChildCalls(exec, 1, "the parent to record the initial child")
		})
	sc.Step(`^the parent starts child flow "([^"]*)" again with child run id "([^"]*)"$`,
		func(ctx context.Context, child, childID string) error {
			w := localStepWorld(ctx)
			exec := w.currentExec
			if exec == nil {
				return fmt.Errorf("no parent run has started")
			}
			reg, ok := w.parentReg(exec.name)
			if !ok {
				return fmt.Errorf("no registration for parent flow %q", exec.name)
			}
			policy := reg.policy
			if policy == "" {
				policy = localDefaultClosePolicy
			}
			want := childCallCount(exec) + 1
			if err := exec.sendChildCommand(localChildCommand{flow: child, id: childID, parentClose: policy}); err != nil {
				return err
			}
			if err := w.waitRecordedChildCalls(exec, want, "the parent to record the reattach child call"); err != nil {
				return err
			}
			if got := childCallCount(exec); got < want {
				return fmt.Errorf("the parent recorded %d child calls after reattach, want %d", got, want)
			}
			return nil
		})
	sc.Step(`^the child call returns the existing child run "([^"]*)"$`, func(ctx context.Context, childID string) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no parent run has started")
		}
		calls := childCallsSnapshot(exec)
		if len(calls) < 2 {
			return fmt.Errorf("the parent recorded %d child calls, want at least 2", len(calls))
		}
		call := calls[1]
		if call.id != childID {
			return fmt.Errorf("the second child call used id %q, want %q", call.id, childID)
		}
		if call.ready {
			if call.readyErr != nil {
				return fmt.Errorf("the second child call was ready with error: %w", call.readyErr)
			}
			return fmt.Errorf("the second child call future was already ready, want a pending reattach")
		}
		tenant := string(w.current.TenantID)
		if tenant == "" {
			return fmt.Errorf("no tenant is recorded for the parent run")
		}
		state, err := w.stateOf(ctx, tenant, childID)
		if err != nil {
			return err
		}
		if state != "running" {
			return fmt.Errorf("child %q is %q after reattach, want running", childID, state)
		}
		return nil
	})
	sc.Step(`^child flow "([^"]*)" was started (\d+) time$`, func(ctx context.Context, name string, want int) error {
		got := localStepWorld(ctx).execCount(name)
		if got != want {
			return fmt.Errorf("child flow %q was started %d times, want %d", name, got, want)
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" has parent run "([^"]*)" with child run "([^"]*)" of flow "([^"]*)"$`,
		func(ctx context.Context, tenant, parentID, childID, childFlow string) error {
			w := localStepWorld(ctx)
			w.setChildGateFlow(childFlow, "approve")
			w.setCommandParent(localDefaultParentFlow, &parentChildReg{
				child: childFlow, id: childID, policy: localDefaultClosePolicy,
			})
			ref, exec, err := w.start(ctx, tenant, parentID, localDefaultParentFlow)
			if err != nil {
				return err
			}
			if err := w.waitRunState(ctx, ref, "running"); err != nil {
				return err
			}
			w.mu.Lock()
			w.current = ref
			w.currentExec = exec
			w.mu.Unlock()
			return w.waitRecordedChildCalls(exec, 1, "the parent to record the initial child")
		})
	sc.Step(`^tenant "([^"]*)" has parent run "([^"]*)" of flow "([^"]*)"$`,
		func(ctx context.Context, tenant, parentID, parentFlow string) error {
			w := localStepWorld(ctx)
			w.setCommandParent(parentFlow, nil)
			ref, exec, err := w.start(ctx, tenant, parentID, parentFlow)
			if err != nil {
				return err
			}
			if err := w.waitRunState(ctx, ref, "running"); err != nil {
				return err
			}
			w.mu.Lock()
			w.current = ref
			w.currentExec = exec
			w.mu.Unlock()
			return nil
		})
	sc.Step(`^parent "([^"]*)" starts child flow "([^"]*)" with child run id "([^"]*)"$`,
		func(ctx context.Context, parentID, child, childID string) error {
			w := localStepWorld(ctx)
			tenant := string(w.current.TenantID)
			if tenant == "" {
				return fmt.Errorf("no tenant is recorded for the parent run")
			}
			exec, err := w.execForRun(tenant, parentID)
			if err != nil {
				return err
			}
			policy := localDefaultClosePolicy
			if reg, ok := w.parentReg(exec.name); ok && reg.policy != "" {
				policy = reg.policy
			}
			want := childCallCount(exec) + 1
			if err := exec.sendChildCommand(localChildCommand{flow: child, id: childID, parentClose: policy}); err != nil {
				return err
			}
			if err := w.waitRecordedChildCalls(exec, want, "the parent to record the child command"); err != nil {
				return err
			}
			if got := childCallCount(exec); got < want {
				return fmt.Errorf("the parent recorded %d child calls, want %d", got, want)
			}
			w.mu.Lock()
			w.currentExec = exec
			w.mu.Unlock()
			return nil
		})
	sc.Step(`^starting the child fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		exec := localStepWorld(ctx).currentExec
		if exec == nil {
			return fmt.Errorf("no parent run is active")
		}
		calls := childCallsSnapshot(exec)
		if len(calls) == 0 {
			return fmt.Errorf("no child call was recorded")
		}
		call := calls[len(calls)-1]
		if !call.ready {
			return fmt.Errorf("the child call future is still pending, want a ready conflict")
		}
		if call.readyErr == nil {
			return fmt.Errorf("the child start succeeded, want code %q", code)
		}
		if localCode(call.readyErr) != code {
			return fmt.Errorf("the child start failed with %q, want %q: %w", localCode(call.readyErr), code, call.readyErr)
		}
		return nil
	})
}

func childCallCount(exec *localExec) int {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	return len(exec.childCalls)
}

func childCallsSnapshot(exec *localExec) []localChildCall {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	return append([]localChildCall(nil), exec.childCalls...)
}

func (w *localWorld) waitRecordedChildCalls(exec *localExec, want int, what string) error {
	if err := w.spin(what, func() bool { return childCallCount(exec) >= want }); err != nil {
		return err
	}
	if got := childCallCount(exec); got < want {
		return fmt.Errorf("the parent recorded %d child calls, want %d", got, want)
	}
	return nil
}
