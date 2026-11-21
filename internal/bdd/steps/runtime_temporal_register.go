package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cucumber/godog"
	sdktemporal "go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/runtime"
)

func registerTemporal(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Temporal; w != nil {
			w.close()
		}
		return ctx, nil
	})

	// Stored for WP-L5-02 (c) server version pinning; unused in data-path scenarios.
	sc.Step(`^a Temporal test server at version "([^"]*)"$`, func(ctx context.Context, version string) error {
		temporalW(ctx).version = version
		return nil
	})
	sc.Step(`^a blob store and a payload codec served by a simulated key service$`, func(ctx context.Context) error {
		return temporalW(ctx).prepare()
	})

	sc.Step(`^step "([^"]*)" fails with code "([^"]*)" marked retryable (true|false) on its first (\d+) executions$`, func(ctx context.Context, name, code, retryable string, times int) error {
		w := temporalW(ctx)
		if err := w.ensureStep(name); err != nil {
			return err
		}
		w.mu.Lock()
		b := w.behaviors[name]
		b.code = core.Code(code)
		b.retryable = retryable == "true"
		b.failFirst = times
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^a run of "([^"]*)" on the temporal runtime runs for 1 day of test time$`, func(ctx context.Context, flow string) error {
		return temporalW(ctx).start("acme", flow, "r1")
	})
	sc.Step(`^a run of "([^"]*)" on the temporal runtime completes$`, func(ctx context.Context, flow string) error {
		return temporalW(ctx).start("acme", flow, "r1")
	})
	sc.Step(`^a run of "([^"]*)" for tenant "([^"]*)" on the temporal runtime completes$`, func(ctx context.Context, flow, tenant string) error {
		return temporalW(ctx).start(tenant, flow, "r1")
	})
	sc.Step(`^a flow calls step "([^"]*)" whose result is (\d+) bytes of seed (\d+) and returns "([^"]*)"$`, func(ctx context.Context, step string, size, seed int, literal string) error {
		return temporalW(ctx).addStepFlow("fetch", step, seedJSON(size, seed), false, json.RawMessage(`"`+literal+`"`))
	})
	sc.Step(`^a flow calls step "([^"]*)" whose result is (\d+) bytes of seed (\d+) and returns it$`, func(ctx context.Context, step string, size, seed int) error {
		return temporalW(ctx).addStepFlow("fetch", step, seedJSON(size, seed), true, nil)
	})
	sc.Step(`^a flow calls step "([^"]*)" whose result is (\{.*\}) and returns it$`, func(ctx context.Context, step, result string) error {
		return temporalW(ctx).addStepFlow("fetch", step, json.RawMessage(result), true, nil)
	})
	sc.Step(`^the payload codec claim-check threshold is (\d+) KB$`, func(ctx context.Context, kb int) error {
		temporalW(ctx).threshold = kb * 1024
		return nil
	})
	sc.Step(`^the run history holds (\d+) claim-checked payloads$`, func(ctx context.Context, want int) error {
		got := 0
		for _, p := range temporalW(ctx).recorded() {
			if p.claimCheck {
				got++
			}
		}
		if got != want {
			return fmt.Errorf("run history holds %d claim-checked payloads, want %d", got, want)
		}
		return nil
	})
	sc.Step(`^every payload in the run history is at most 5 KB$`, func(ctx context.Context) error {
		for _, p := range temporalW(ctx).recorded() {
			if len(p.data) > 5*1024 {
				return fmt.Errorf("payload is %d bytes, want at most 5 KB", len(p.data))
			}
		}
		return nil
	})
	sc.Step(`^the run history holds at least (\d+) payload, and every payload has encoding "([^"]*)"$`, func(ctx context.Context, least int, encoding string) error {
		n := 0
		for _, p := range temporalW(ctx).recorded() {
			n++
			if got := string(p.metadata["encoding"]); got != encoding {
				return fmt.Errorf("encoding %q, want %q", got, encoding)
			}
		}
		if n < least {
			return fmt.Errorf("run history holds %d payloads, want at least %d", n, least)
		}
		return nil
	})
	sc.Step(`^the run history holds neither "([^"]*)" nor "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		for _, p := range temporalW(ctx).recorded() {
			if bytes.Contains(p.data, []byte(first)) {
				return fmt.Errorf("payload holds %q", first)
			}
			if string(p.metadata["encoding"]) == second {
				return fmt.Errorf("payload encoding %q", second)
			}
		}
		return nil
	})

	sc.Step(`^step "([^"]*)" recorded an unencrypted payload in the run history of a run on the temporal runtime$`, func(ctx context.Context, step string) error {
		w := temporalW(ctx)
		w.faultMode = "plain"
		if err := w.addStepFlow("fetch", step, json.RawMessage(`{"ok":true}`), true, nil); err != nil {
			return err
		}
		return w.start("acme", "fetch", "r1")
	})
	sc.Step(`^tenant "([^"]*)" has stored a blob of (\d+) bytes of seed (\d+)$`, func(ctx context.Context, tenant string, size, seed int) error {
		w := temporalW(ctx)
		if err := w.ensureRuntime(); err != nil {
			return err
		}
		content := seedBytes(size, seed)
		key := blob.ContentKeyOf(content)
		ctxT := core.WithTenant(context.Background(), core.TenantID(tenant))
		if err := w.blobs.Put(ctxT, key, bytes.NewReader(content), "application/octet-stream"); err != nil {
			return err
		}
		w.faultMode = "foreign"
		w.faultKey = key
		return w.ensureStep("fetch")
	})
	sc.Step(`^a run on the temporal runtime for tenant "([^"]*)" has a step "([^"]*)" result in its run history naming that blob's content key$`, func(ctx context.Context, tenant, step string) error {
		w := temporalW(ctx)
		if err := w.addStepFlow("fetch", step, json.RawMessage(`{"ok":true}`), true, nil); err != nil {
			return err
		}
		return w.start(tenant, "fetch", "r1")
	})
	sc.Step(`^the run reads the result of step "([^"]*)"$`, func(ctx context.Context, _ string) error {
		temporalW(ctx).waitDone()
		return nil
	})
}

func temporalStepFailure(w *temporalWorld, code string) error {
	w.mu.Lock()
	err := w.lastStepErr
	if err == nil {
		err = w.lastErr
	}
	w.mu.Unlock()
	if err != nil && string(core.CodeOf(err)) == code {
		return nil
	}
	if err == nil && w.env != nil {
		err = w.env.GetWorkflowError()
	}
	if err == nil {
		return fmt.Errorf("step succeeded, want %s", code)
	}
	got := temporalWorkflowErrCode(err)
	if got != code {
		return fmt.Errorf("step code %q, want %q", got, code)
	}
	return nil
}

func temporalWorkflowErrCode(err error) string {
	if err == nil {
		return ""
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return string(pe.Code)
	}
	var app *sdktemporal.ApplicationError
	if errors.As(err, &app) {
		return app.Type()
	}
	var wf *sdktemporal.WorkflowExecutionError
	if errors.As(err, &wf) {
		return temporalWorkflowErrCode(wf.Unwrap())
	}
	return string(core.CodeOf(err))
}

func (w *temporalWorld) addFlow(name string, fn func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error)) error {
	if err := w.ensureRuntime(); err != nil {
		return err
	}
	w.mu.Lock()
	w.flows[name] = fn
	w.mu.Unlock()
	return nil
}

func (w *temporalWorld) addStepFlow(name, step string, output json.RawMessage, returnIt bool, literal json.RawMessage) error {
	if err := w.ensureStep(step); err != nil {
		return err
	}
	w.mu.Lock()
	w.behaviors[step].output = append(json.RawMessage(nil), output...)
	w.mu.Unlock()
	return w.addFlow(name, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var got json.RawMessage
		err := dc.Step(step, nil, runtime.StepOptions{}).Get(&got)
		w.mu.Lock()
		w.lastStepErr = err
		w.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if returnIt {
			return got, nil
		}
		return append(json.RawMessage(nil), literal...), nil
	})
}

func (w *temporalWorld) ensureNamedFlow(name string) error {
	w.mu.Lock()
	_, ok := w.flows[name]
	w.mu.Unlock()
	if ok {
		return nil
	}
	switch name {
	case "pay":
		if err := w.ensureStep("charge"); err != nil {
			return err
		}
		w.mu.Lock()
		w.behaviors["charge"].output = json.RawMessage(`{"ok":true}`)
		w.mu.Unlock()
		return w.addFlow("pay", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			var out json.RawMessage
			err := dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: w.attempts}).Get(&out)
			w.mu.Lock()
			w.lastStepErr = err
			w.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return out, nil
		})
	case "fetch":
		if err := w.ensureStep("fetch"); err != nil {
			return err
		}
		return w.addFlow("fetch", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			var out json.RawMessage
			err := dc.Step("fetch", nil, runtime.StepOptions{}).Get(&out)
			w.mu.Lock()
			w.lastStepErr = err
			w.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return out, nil
		})
	}
	return nil
}
