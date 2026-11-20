package steps

import (
	"context"
	"errors"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
)

// retentionWorld is the state of the scenarios that bound a registry's memory
// (REQ-MODEL-088). It drives a real model.Registry on the scenario's fake
// clock: every "N minutes pass" step advances that same clock, and every
// registry method sweeps it.
//
// started counts accepted vendor calls: a Begin that opens a new run is one,
// and one that replays a retained run publishes nothing and costs nothing.
type retentionWorld struct {
	clock   *coretest.FakeClock
	reg     *model.Registry
	refs    map[string]*model.Run
	last    string
	started int
}

func newRetentionWorld() *retentionWorld {
	return &retentionWorld{refs: map[string]*model.Run{}}
}

// start begins an invocation for ref through the registry and remembers it.
func (w *retentionWorld) start(ctx context.Context, ref string) error {
	if w.reg == nil {
		return fmt.Errorf("scenario built no registry; a \"a registry with a retention window of …\" Given comes first")
	}
	run, existing, err := w.reg.Begin(ctx, ref)
	if err != nil {
		return err
	}
	if !existing {
		w.started++
	}
	w.refs[ref] = run
	w.last = ref
	return nil
}

// handle is the handle of the invocation last started or ended for ref.
func (w *retentionWorld) handle(ref string) (model.Handle, error) {
	run, ok := w.refs[ref]
	if !ok {
		return model.Handle{}, fmt.Errorf("scenario never started client ref %q", ref)
	}
	return run.Handle(), nil
}

// lastHandle is the handle of the invocation the last step named.
func (w *retentionWorld) lastHandle() (model.Handle, error) {
	if w.last == "" {
		return model.Handle{}, fmt.Errorf("scenario started no invocation")
	}
	return w.handle(w.last)
}

func registerModelRetention(sc *godog.ScenarioContext) {
	sc.Step(`^a registry with a retention window of (\d+ (?:second|minute|hour|day)s?)$`,
		func(ctx context.Context, window string) error {
			return world(ctx).Model.configureRegistry(ctx, window, 0)
		})
	sc.Step(`^a registry with a retention window of (\d+ (?:second|minute|hour|day)s?) and an ended bound of (\d+)$`,
		func(ctx context.Context, window string, bound int) error {
			return world(ctx).Model.configureRegistry(ctx, window, bound)
		})
	sc.Step(`^a caller starts an invocation with client ref "([^"]*)" that streams "([^"]*)" in (\d+) events and ends as succeeded$`,
		func(ctx context.Context, ref, text string, chunks int) error {
			w := world(ctx).Model
			if err := w.startRetention(ctx, ref); err != nil {
				return err
			}
			run := w.retention.refs[ref]
			for _, chunk := range textChunks(text, chunks) {
				run.Publish(model.Event{Kind: model.EventText, Text: chunk})
			}
			run.Succeed(messageResult(text))
			return nil
		})
	sc.Step(`^a caller starts an invocation with client ref "([^"]*)" that ends as succeeded$`,
		func(ctx context.Context, ref string) error {
			w := world(ctx).Model
			if err := w.startRetention(ctx, ref); err != nil {
				return err
			}
			w.retention.refs[ref].Succeed(messageResult("Hello"))
			return nil
		})
	sc.Step(`^a caller starts an invocation with client ref "([^"]*)" that stays running$`,
		func(ctx context.Context, ref string) error {
			return world(ctx).Model.startRetention(ctx, ref)
		})
	sc.Step(`^(\d+) other invocations start and end$`, func(ctx context.Context, n int) error {
		w := world(ctx).Model.retention
		for i := 1; i <= n; i++ {
			ref := fmt.Sprintf("other-%d", i)
			if err := w.start(world(ctx).Model.tenantCtx(ctx), ref); err != nil {
				return err
			}
			w.refs[ref].Succeed(messageResult("Hello"))
		}
		return nil
	})
	sc.Step(`^the invocation with client ref "([^"]*)" ends as succeeded$`,
		func(ctx context.Context, ref string) error {
			w := world(ctx).Model.retention
			run, ok := w.refs[ref]
			if !ok {
				return fmt.Errorf("scenario never started client ref %q", ref)
			}
			select {
			case <-run.Done():
				return fmt.Errorf("invocation %q had already ended", ref)
			default:
			}
			run.Succeed(messageResult("Hello"))
			w.last = ref
			return nil
		})
	sc.Step(`^the lookup of "([^"]*)" finds its invocation$`, func(ctx context.Context, ref string) error {
		w := world(ctx).Model.retention
		want, err := w.handle(ref)
		if err != nil {
			return err
		}
		got, err := w.reg.Lookup(world(ctx).Model.tenantCtx(ctx), ref)
		if err != nil {
			return fmt.Errorf("lookup %q: %w", ref, err)
		}
		if got == nil {
			return fmt.Errorf("lookup %q found no invocation, want its invocation", ref)
		}
		if *got != want {
			return fmt.Errorf("lookup %q = %+v, want %+v", ref, *got, want)
		}
		return nil
	})
	sc.Step(`^the lookup of "([^"]*)" finds no invocation$`, func(ctx context.Context, ref string) error {
		w := world(ctx).Model.retention
		got, err := w.reg.Lookup(world(ctx).Model.tenantCtx(ctx), ref)
		if err != nil {
			return fmt.Errorf("lookup %q: %w", ref, err)
		}
		if got != nil {
			return fmt.Errorf("lookup %q = %+v, want no invocation", ref, *got)
		}
		return nil
	})
	sc.Step(`^following its events from the start yields (\d+) text events$`,
		func(ctx context.Context, want int) error {
			w := world(ctx).Model.retention
			h, err := w.lastHandle()
			if err != nil {
				return err
			}
			ch, err := w.reg.Events(world(ctx).Model.tenantCtx(ctx), h, 0)
			if err != nil {
				return fmt.Errorf("events: %w", err)
			}
			count := 0
			for e := range ch {
				if e.Kind == model.EventText {
					count++
				}
			}
			if count != want {
				return fmt.Errorf("received %d text events, want %d", count, want)
			}
			return nil
		})
	sc.Step(`^following its events fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx).Model.retention
		h, err := w.lastHandle()
		if err != nil {
			return err
		}
		if _, err := w.reg.Events(world(ctx).Model.tenantCtx(ctx), h, 0); failureCode(err) != code {
			return fmt.Errorf("events code = %q, want %q", failureCode(err), code)
		}
		return nil
	})
	sc.Step(`^waiting for its result fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx).Model.retention
		h, err := w.lastHandle()
		if err != nil {
			return err
		}
		if _, err := w.reg.Result(world(ctx).Model.tenantCtx(ctx), h); failureCode(err) != code {
			return fmt.Errorf("result code = %q, want %q", failureCode(err), code)
		}
		return nil
	})
	sc.Step(`^cancelling it fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx).Model.retention
		h, err := w.lastHandle()
		if err != nil {
			return err
		}
		if err := w.reg.Cancel(world(ctx).Model.tenantCtx(ctx), h); failureCode(err) != code {
			return fmt.Errorf("cancel code = %q, want %q", failureCode(err), code)
		}
		return nil
	})
	sc.Step(`^its result has state "([^"]*)"$`, func(ctx context.Context, want string) error {
		w := world(ctx).Model.retention
		h, err := w.lastHandle()
		if err != nil {
			return err
		}
		res, err := w.reg.Result(world(ctx).Model.tenantCtx(ctx), h)
		if err != nil {
			return fmt.Errorf("result: %w", err)
		}
		if got := string(res.State); got != want {
			return fmt.Errorf("result state is %q, want %q", got, want)
		}
		return nil
	})
	sc.Step(`^the provider accepted (\d+) invocations$`, acceptedInvocations)
}

// acceptedInvocations owns the phrase for every model world, because godog has
// one step namespace and the registry scenarios and the provider scenarios say
// it the same way. The registry case counts Begin calls that opened a run; the
// provider case counts the starts a simulated adapter passed to its vendor.
func acceptedInvocations(ctx context.Context, want int) error {
	w := world(ctx).Model
	got := w.accepted
	if w.call != nil {
		got = len(w.call.invocations())
	}
	if w.retention.reg != nil {
		got = w.retention.started
	}
	if got != want {
		return fmt.Errorf("provider accepted %d invocations, want %d", got, want)
	}
	return nil
}

// configureRegistry builds the scenario's registry on the frozen clock, with
// the bounds its Given names. bound 0 keeps the ended-bound default.
func (w *modelWorld) configureRegistry(ctx context.Context, window string, bound int) error {
	d, err := parseElapsed(window)
	if err != nil {
		return err
	}
	w.retention.clock = world(ctx).Core.clock
	reg, err := model.NewRegistry("sim",
		model.WithClock(w.retention.clock),
		model.WithRetention(d, bound))
	if err != nil {
		return err
	}
	w.retention.reg = reg
	return nil
}

func (w *modelWorld) startRetention(ctx context.Context, ref string) error {
	if w.retention.reg == nil {
		return fmt.Errorf("no registry was built for this scenario")
	}
	return w.retention.start(world(ctx).Model.tenantCtx(ctx), ref)
}

// failureCode is the platform error code of err, or empty when err is nil or
// is not a platform error.
func failureCode(err error) string {
	if err == nil {
		return ""
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		return ""
	}
	return string(pe.Code)
}
