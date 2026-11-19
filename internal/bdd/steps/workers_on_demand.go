package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model/worker"
	"github.com/epzero/basamento/model/worker/scale"
)

// registerWorkerOnDemand holds the steps of ADR-0012 §12: worker templates,
// the scaler, single-use join tokens and a launched worker's own exit.
func registerWorkerOnDemand(sc *godog.ScenarioContext) {
	sc.Step(`^pool "([^"]*)" has worker template "([^"]*)" serving model "([^"]*)" with capacity (\d+), minimum (\d+), maximum (\d+) and a cold start of (\d+) minutes?$`, createTemplate)
	sc.Step(`^the shared pool has worker template "([^"]*)" serving model "([^"]*)" with capacity (\d+), minimum (\d+), maximum (\d+) and a cold start of (\d+) minutes?$`,
		func(ctx context.Context, templateID, modelID string, capacity, min, max, cold int) error {
			return createTemplate(ctx, "shared", templateID, modelID, capacity, min, max, cold)
		})
	sc.Step(`^template "([^"]*)" uses a simulated launcher$`, func(ctx context.Context, templateID string) error {
		w := world(ctx).Worker
		tmpl, ok := w.templates[templateID]
		if !ok {
			return fmt.Errorf("template %q does not exist", templateID)
		}
		tmpl.Launcher = "simulated"
		return saveTemplate(ctx, tmpl)
	})
	sc.Step(`^pool "([^"]*)" has no present worker$`, func(ctx context.Context, poolID string) error {
		_, err := world(ctx).Worker.ensurePool(ctx, poolID)
		return err
	})
	sc.Step(`^the deployment's cap is (\d+) workers? on demand$`, func(ctx context.Context, n int) error {
		w := world(ctx).Worker
		w.deploymentCap = n
		w.rebuildScaler(ctx)
		return nil
	})
	sc.Step(`^tenant "([^"]*)" (?:queues|queued) ([2-9]|[1-9][0-9]+)(?: more)? invocations? of "([^"]*)" for (?:pool "([^"]*)"|the shared pool)$`, queueForScale)
	sc.Step(`^the scaler runs with no invocation waiting$`, tick)
	sc.Step(`^the launcher was asked for (\d+) workers? of template "([^"]*)"$`, func(ctx context.Context, want int, templateID string) error {
		if err := tick(ctx); err != nil {
			return err
		}
		return assertAsked(ctx, templateID, want)
	})
	sc.Step(`^the launcher was asked for (\d+) workers? of template "([^"]*)", which has not joined yet$`, func(ctx context.Context, want int, templateID string) error {
		if err := tick(ctx); err != nil {
			return err
		}
		if err := assertAsked(ctx, templateID, want); err != nil {
			return err
		}
		return assertNoJoin(ctx, templateID)
	})
	sc.Step(`^the launcher was asked for (\d+) workers? of template "([^"]*)" with join token "([^"]*)"$`, func(ctx context.Context, want int, templateID, token string) error {
		if err := tick(ctx); err != nil {
			return err
		}
		if world(ctx).Worker.launcher.Asked(templateID) == 0 {
			if err := askOneWorker(ctx, templateID); err != nil {
				return err
			}
		}
		if err := assertAsked(ctx, templateID, want); err != nil {
			return err
		}
		return assertToken(ctx, templateID, token)
	})
	sc.Step(`^the launcher was asked for (\d+) workers? of template "([^"]*)" in total$`, func(ctx context.Context, want int, templateID string) error {
		return assertAsked(ctx, templateID, want)
	})
	sc.Step(`^the launcher was asked for (\d+) workers? of template "([^"]*)" with join token "([^"]*)" and a cold-start deadline of (\d+) minutes?$`,
		func(ctx context.Context, want int, templateID, token string, minutes int) error {
			w := world(ctx).Worker
			tmpl, ok := w.templates[templateID]
			if !ok {
				return fmt.Errorf("template %q does not exist", templateID)
			}
			tmpl.ColdStart = time.Duration(minutes) * time.Minute
			if err := saveTemplate(ctx, tmpl); err != nil {
				return err
			}
			if err := tick(ctx); err != nil {
				return err
			}
			if world(ctx).Worker.launcher.Asked(templateID) == 0 {
				if err := askOneWorker(ctx, templateID); err != nil {
					return err
				}
			}
			if err := assertAsked(ctx, templateID, want); err != nil {
				return err
			}
			return assertToken(ctx, templateID, token)
		})
	sc.Step(`^the catalog lists "([^"]*)" with availability "([^"]*)" and a cold start of (\d+) minutes?$`,
		func(ctx context.Context, modelID, availability string, minutes int) error {
			w := world(ctx).Worker
			st, err := w.open(ctx)
			if err != nil {
				return err
			}
			pool := workerPoolForModel(ctx, modelID)
			catalog, err := st.Catalog(ctx, pool)
			if err != nil {
				return err
			}
			for _, entry := range catalog {
				if entry.Descriptor.ID != modelID {
					continue
				}
				if string(entry.Availability) != availability {
					return fmt.Errorf("%q is %s, want %s", modelID, entry.Availability, availability)
				}
				if entry.ColdStart != time.Duration(minutes)*time.Minute {
					return fmt.Errorf("%q has a cold start of %v, want %d minutes", modelID, entry.ColdStart, minutes)
				}
				return nil
			}
			return fmt.Errorf("the catalog does not list %q", modelID)
		})
	sc.Step(`^a worker of template "([^"]*)" is present with (\d+) free units?$`, func(ctx context.Context, templateID string, free int) error {
		w := world(ctx).Worker
		tmpl, ok := w.templates[templateID]
		if !ok {
			return fmt.Errorf("template %q does not exist", templateID)
		}
		if err := w.joinFromTemplate(ctx, tmpl); err != nil {
			return err
		}
		workers, err := mustStore(ctx).ListWorkers(ctx, tmpl.Pool)
		if err != nil {
			return err
		}
		for _, got := range workers {
			if got.Present && got.Capacity-got.UnitsInUse == free {
				return nil
			}
		}
		return fmt.Errorf("no present worker of %q has %d free units", templateID, free)
	})
	sc.Step(`^a worker of template "([^"]*)" is present with an idle-exit time of (\d+) minutes and no lease$`,
		func(ctx context.Context, templateID string, minutes int) error {
			w := world(ctx).Worker
			tmpl, ok := w.templates[templateID]
			if !ok {
				return fmt.Errorf("template %q does not exist", templateID)
			}
			tmpl.IdleExit = time.Duration(minutes) * time.Minute
			if err := saveTemplate(ctx, tmpl); err != nil {
				return err
			}
			joined, err := w.joinFromTemplateWorker(ctx, tmpl)
			if err != nil {
				return err
			}
			now := world(ctx).Core.clock.Now()
			w.launched["launched"] = &launchedWorker{
				worker: joined, credential: joined.Credential, started: now, lastWork: now,
				policy: scale.ExitPolicy{IdleExit: tmpl.IdleExit, MaxLifetime: tmpl.MaxLifetime, MaxInvocations: tmpl.MaxInvocations},
			}
			return nil
		})
	sc.Step(`^that worker has left pool "([^"]*)"$`, func(ctx context.Context, poolID string) error {
		w := world(ctx).Worker
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		now := world(ctx).Core.clock.Now()
		found := false
		for name, launched := range w.launched {
			if launched.worker.Pool != poolID {
				continue
			}
			found = true
			if launched.policy.Leave(now, launched.started, launched.lastWork, launched.invocations) {
				if err := st.Leave(ctx, launched.worker.ID, launched.credential); err != nil {
					return err
				}
				delete(w.launched, name)
			}
		}
		if !found {
			return fmt.Errorf("no launched worker of pool %q is present", poolID)
		}
		workers, err := st.ListWorkers(ctx, poolID)
		if err != nil {
			return err
		}
		for _, got := range workers {
			if got.Present {
				return fmt.Errorf("worker %s is still present in pool %q", got.ID, poolID)
			}
		}
		return nil
	})
	sc.Step(`^a worker joined pool "([^"]*)" with join token "([^"]*)"$`, func(ctx context.Context, poolID, token string) error {
		return joinWithToken(ctx, poolID, token, "", 0)
	})
	sc.Step(`^a worker joins pool "([^"]*)" with join token "([^"]*)"$`, func(ctx context.Context, poolID, token string) error {
		return joinWithToken(ctx, poolID, token, "", 0)
	})
	sc.Step(`^another worker joins pool "([^"]*)" with join token "([^"]*)"$`, func(ctx context.Context, poolID, token string) error {
		return joinWithToken(ctx, poolID, token, "", 0)
	})
	sc.Step(`^a worker joins pool "([^"]*)" with join token "([^"]*)" declaring model "([^"]*)" with capacity (\d+)$`, joinWithToken)
	sc.Step(`^a run invoked "([^"]*)" with a queue timeout of (\d+) minutes$`, func(ctx context.Context, modelID string, minutes int) error {
		w := world(ctx).Worker
		return w.startRun(ctx, core.TenantID("acme"), workerPoolForModel(ctx, modelID), modelID, time.Duration(minutes)*time.Minute, 0)
	})
	sc.Step(`^the simulated launcher accepts every request but no worker ever joins, with last error "([^"]*)"$`,
		func(ctx context.Context, lastError string) error {
			w := world(ctx).Worker
			if _, err := w.open(ctx); err != nil {
				return err
			}
			w.launcher.SetAccept(true)
			w.launcher.SetLastLaunchError(lastError)
			return nil
		})
	sc.Step(`^(\d+) launches of template "([^"]*)" in a row pass their cold-start deadlines$`,
		func(ctx context.Context, n int, templateID string) error {
			for i := 0; i < 120; i++ {
				if err := tick(ctx); err != nil {
					return err
				}
				state, err := mustScaler(ctx).TemplateState(ctx, templateID)
				if err != nil {
					return err
				}
				if state == scale.StateDegraded {
					return nil
				}
				advanceScenarioClock(ctx, 30*time.Second)
			}
			return fmt.Errorf("template %q did not degrade after %d launches", templateID, n)
		})
	sc.Step(`^template "([^"]*)" is "([^"]*)"$`, func(ctx context.Context, templateID, want string) error {
		state, err := mustScaler(ctx).TemplateState(ctx, templateID)
		if err != nil {
			return err
		}
		if string(state) != want {
			return fmt.Errorf("template %q is %q, want %q", templateID, state, want)
		}
		return nil
	})
	sc.Step(`^when the queue timeout passes, the node fails with code "([^"]*)" and message "([^"]*)"$`,
		func(ctx context.Context, code, message string) error {
			w := world(ctx).Worker
			if w.run == nil {
				return fmt.Errorf("no run is in flight")
			}
			st, err := w.open(ctx)
			if err != nil {
				return err
			}
			inv, err := st.Invocation(ctx, w.run.clientRef)
			if err != nil {
				return err
			}
			if inv.QueueTimeout > 0 {
				deadline := inv.QueuedAt.Add(inv.QueueTimeout)
				if now := world(ctx).Core.clock.Now(); now.Before(deadline) {
					advanceScenarioClock(ctx, deadline.Sub(now)+time.Second)
				}
			} else {
				advanceScenarioClock(ctx, 31*time.Minute)
			}
			if err := st.Sweep(ctx); err != nil {
				return err
			}
			inv, err = st.Invocation(ctx, w.run.clientRef)
			if err != nil {
				return err
			}
			if inv.State != "failed" {
				return fmt.Errorf("invocation %q is %s, want failed", inv.ClientRef, inv.State)
			}
			if string(inv.Code) != code {
				return fmt.Errorf("got code %s, want %s", inv.Code, code)
			}
			if inv.Message != message {
				return fmt.Errorf("got message %q, want %q", inv.Message, message)
			}
			w.run.err = core.NewError(inv.Code, inv.Message, nil)
			w.run.settled = true
			return nil
		})
}

func createTemplate(ctx context.Context, poolID, templateID, modelID string, capacity, min, max, cold int) error {
	w := world(ctx).Worker
	if _, err := w.ensurePool(ctx, poolID); err != nil {
		return err
	}
	tmpl := scale.Template{
		ID: templateID, Pool: poolID,
		Descriptors: []worker.Descriptor{descriptorForModel(modelID, 1, false)},
		Capacity:    capacity, Min: min, Max: max,
		ColdStart: time.Duration(cold) * time.Minute,
		Launcher:  "simulated",
	}
	return saveTemplate(ctx, tmpl)
}

func saveTemplate(ctx context.Context, tmpl scale.Template) error {
	w := world(ctx).Worker
	tokens, err := w.tokenStore(ctx)
	if err != nil {
		return err
	}
	if err := tokens.SaveTemplate(ctx, tmpl); err != nil {
		return err
	}
	saved, err := tokens.Template(ctx, tmpl.ID)
	if err != nil {
		return err
	}
	w.templates[tmpl.ID] = saved
	return nil
}

func queueForScale(ctx context.Context, tenant string, n int, modelID, poolID string) error {
	if poolID == "" {
		poolID = "shared"
	}
	w := world(ctx).Worker
	for i := 0; i < n; i++ {
		if _, err := w.queued(ctx, core.TenantID(tenant), poolID, modelID, "", w.nextRef("q")); err != nil {
			return err
		}
	}
	return tick(ctx)
}

func tick(ctx context.Context) error {
	scaler := mustScaler(ctx)
	if scaler == nil {
		return fmt.Errorf("no scaler")
	}
	return scaler.Tick(ctx)
}

func mustScaler(ctx context.Context) *scale.Scaler {
	w := world(ctx).Worker
	if _, err := w.open(ctx); err != nil {
		return nil
	}
	if w.scaler == nil {
		w.rebuildScaler(ctx)
	}
	return w.scaler
}

func mustStore(ctx context.Context) *worker.Store {
	w := world(ctx).Worker
	st, _ := w.open(ctx)
	return st
}

// askOneWorker makes a launch happen for a scenario that states the launch as
// a given. It uses the template's minimum, so the production scaler is the
// only thing that asks the launcher.
func askOneWorker(ctx context.Context, templateID string) error {
	w := world(ctx).Worker
	tmpl, ok := w.templates[templateID]
	if !ok {
		return fmt.Errorf("template %q does not exist", templateID)
	}
	original := tmpl
	tmpl.Min = 1
	if err := saveTemplate(ctx, tmpl); err != nil {
		return err
	}
	if err := tick(ctx); err != nil {
		return err
	}
	return saveTemplate(ctx, original)
}

func assertAsked(ctx context.Context, templateID string, want int) error {
	got := world(ctx).Worker.launcher.Asked(templateID)
	if got != want {
		return fmt.Errorf("the launcher was asked for %d workers of %q, want %d", got, templateID, want)
	}
	return nil
}

func assertToken(ctx context.Context, templateID, token string) error {
	tokens := world(ctx).Worker.launcher.Tokens(templateID)
	for _, got := range tokens {
		if got == token {
			return nil
		}
	}
	return fmt.Errorf("the launcher was asked for %q with tokens %v, want %q", templateID, tokens, token)
}

func assertNoJoin(ctx context.Context, templateID string) error {
	w := world(ctx).Worker
	tmpl, ok := w.templates[templateID]
	if !ok {
		return fmt.Errorf("template %q does not exist", templateID)
	}
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	workers, err := st.ListWorkers(ctx, tmpl.Pool)
	if err != nil {
		return err
	}
	for _, got := range workers {
		if got.Present && got.Template == templateID {
			return fmt.Errorf("a worker of %q has already joined", templateID)
		}
	}
	return nil
}

func joinWithToken(ctx context.Context, poolID, token, modelID string, capacity int) error {
	w := world(ctx).Worker
	if _, err := w.ensurePool(ctx, poolID); err != nil {
		return err
	}
	tmpl, err := templateForToken(ctx, token)
	if err != nil {
		return err
	}
	descriptors := tmpl.Descriptors
	if modelID != "" {
		descriptors = []worker.Descriptor{descriptorForModel(modelID, 1, false)}
	}
	if capacity == 0 {
		capacity = tmpl.Capacity
	}
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	w.lastErr = nil
	joined, err := st.Join(ctx, worker.JoinRequest{
		PoolToken:       token,
		ProtocolVersion: worker.ProtocolVersion,
		Descriptors:     descriptors,
		Labels:          tmpl.Labels,
		Capacity:        capacity,
	})
	if err != nil {
		w.lastErr = err
		return nil
	}
	w.workers["launched"] = &joinedWorker{worker: joined, credential: joined.Credential}
	w.workerOrder = append(w.workerOrder, "launched")
	return nil
}

// templateForToken finds the template a launch token belongs to by asking the
// launch store for the grant without spending the token. The store is local,
// so this reads its row directly; the join itself spends the token.
func templateForToken(ctx context.Context, token string) (scale.Template, error) {
	w := world(ctx).Worker
	tokens, err := w.tokenStore(ctx)
	if err != nil {
		return scale.Template{}, err
	}
	return tokens.TemplateForToken(ctx, token)
}

func (w *workerWorld) joinFromTemplate(ctx context.Context, tmpl scale.Template) error {
	_, err := w.joinFromTemplateWorker(ctx, tmpl)
	return err
}

func (w *workerWorld) joinFromTemplateWorker(ctx context.Context, tmpl scale.Template) (worker.Worker, error) {
	tokens, err := w.tokenStore(ctx)
	if err != nil {
		return worker.Worker{}, err
	}
	_, minted, err := tokens.MintLaunch(ctx, tmpl, 1, world(ctx).Core.clock.Now().Add(tmpl.ColdStart))
	if err != nil {
		return worker.Worker{}, err
	}
	st, err := w.open(ctx)
	if err != nil {
		return worker.Worker{}, err
	}
	joined, err := st.Join(ctx, worker.JoinRequest{
		PoolToken: minted[0], ProtocolVersion: worker.ProtocolVersion,
		Descriptors: tmpl.Descriptors, Labels: tmpl.Labels, Capacity: tmpl.Capacity,
	})
	if err != nil {
		return worker.Worker{}, err
	}
	w.workers["launched"] = &joinedWorker{worker: joined, credential: joined.Credential}
	w.workerOrder = append(w.workerOrder, "launched")
	return joined, nil
}
