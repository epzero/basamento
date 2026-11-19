package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/browser/browsertest"
	"github.com/epzero/basamento/browser/conformance"
)

type browserWorld struct {
	runner  conformance.Runner
	app     *conformance.App
	model   *conformance.ScriptedModel
	obs     *conformance.Observation
	feature string
	value   string
	goal    string
	budget  conformance.Budget
	step    string
}

func newBrowserWorld() *browserWorld { return &browserWorld{} }

// browserModelToken is the session token the scripted platform endpoint
// accepts; the runner must present exactly this one.
const browserModelToken = "session-token"

func registerBrowser(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		w := world(ctx).Browser
		if w.app != nil {
			w.app.Close()
		}
		if w.model != nil {
			w.model.Close()
		}
		return ctx, nil
	})

	sc.Step(`^a browser runner under test and the fixture app "([^"]*)"$`, func(ctx context.Context, app string) error {
		return world(ctx).Browser.fixture(app, false)
	})
	sc.Step(`^a browser runner under test and the fixture app "([^"]*)" with a broken delete button$`, func(ctx context.Context, app string) error {
		return world(ctx).Browser.fixture(app, true)
	})
	sc.Step(`^a browser runner under test, a scripted model on the platform endpoint, and the fixture app "([^"]*)"$`, func(ctx context.Context, app string) error {
		w := world(ctx).Browser
		if err := w.fixture(app, false); err != nil {
			return err
		}
		w.model = conformance.NewScriptedModel(browserModelToken)
		return nil
	})
	sc.Step(`^the scripted model never declares the goal reached$`, func(context.Context) error {
		// The scripted endpoint only declares the goal reached when asked to.
		return nil
	})

	sc.Step(`^a scripted session is started for feature "([^"]*)"$`, func(ctx context.Context, name string) error {
		return world(ctx).Browser.runScripted(ctx, name, conformance.Recording{})
	})
	sc.Step(`^a scripted session is started with recording "([^"]*)"$`, func(ctx context.Context, recording string) error {
		w := world(ctx).Browser
		rec, err := recordingFor(recording)
		if err != nil {
			return err
		}
		return w.runScripted(ctx, "todo/add-item.feature", rec)
	})
	sc.Step(`^a scripted session types "([^"]*)" into the password field with recording "([^"]*)"$`, func(ctx context.Context, value, recording string) error {
		w := world(ctx).Browser
		rec, err := recordingFor(recording)
		if err != nil {
			return err
		}
		w.value = value
		w.feature = "Feature: Sign in\n  Scenario: ana signs in\n    When ana types \"" + value + "\" into the password field\n"
		return w.startScripted(ctx, w.feature, rec)
	})
	sc.Step(`^a goal-driven session is started with goal "([^"]*)" and a budget of (\d+) steps$`, func(ctx context.Context, goal string, steps int) error {
		w := world(ctx).Browser
		w.goal = goal
		w.budget = conformance.Budget{MaxSteps: int32(steps)}
		return w.startGoal(ctx)
	})
	sc.Step(`^a goal-driven session is started with goal "([^"]*)"$`, func(ctx context.Context, goal string) error {
		w := world(ctx).Browser
		w.goal = goal
		w.budget = conformance.Budget{}
		return w.startGoal(ctx)
	})

	sc.Step(`^the session reports progress events and ends with state "([^"]*)"$`, func(ctx context.Context, state string) error {
		return world(ctx).Browser.obs.ProgressAndState(conformance.State(state))
	})
	sc.Step(`^each step of the feature has a reported result$`, func(ctx context.Context) error {
		w := world(ctx).Browser
		return w.obs.EveryStepReported(w.feature)
	})
	sc.Step(`^the artifacts include an rrweb recording$`, func(ctx context.Context) error {
		_, err := world(ctx).Browser.obs.DOMRecording(ctx)
		return err
	})
	sc.Step(`^every action event falls within the recording's time range$`, func(ctx context.Context) error {
		return world(ctx).Browser.obs.ActionsWithinRecording(ctx)
	})
	sc.Step(`^the rrweb recording does not contain "([^"]*)"$`, func(ctx context.Context, value string) error {
		return world(ctx).Browser.obs.RecordingDoesNotContain(ctx, value)
	})
	sc.Step(`^the step "([^"]*)" is reported failed$`, func(ctx context.Context, step string) error {
		w := world(ctx).Browser
		w.step = step
		_, err := w.obs.FailedStep(step)
		return err
	})
	sc.Step(`^the failure carries a screenshot and the rrweb timestamp$`, func(ctx context.Context) error {
		w := world(ctx).Browser
		if w.step == "" {
			return fmt.Errorf("no failed step was reported")
		}
		return w.obs.FailureEvidence(ctx, w.step)
	})
	sc.Step(`^the session ends after (\d+) actions with state "([^"]*)" and reason "([^"]*)"$`, func(ctx context.Context, steps int, state, reason string) error {
		w := world(ctx).Browser
		if err := w.obs.BudgetExhausted(steps); err != nil {
			return err
		}
		if got := string(w.obs.Result.State); got != state {
			return fmt.Errorf("session state is %q, want %q", got, state)
		}
		if got := w.obs.Result.Reason; got != reason {
			return fmt.Errorf("session reason is %q, want %q", got, reason)
		}
		return nil
	})
	sc.Step(`^every action is reported with its target and a screenshot$`, func(ctx context.Context) error {
		return world(ctx).Browser.obs.ActionsHaveEvidence(ctx)
	})
	sc.Step(`^every model call the runner made reached the platform endpoint with that session's token$`, func(ctx context.Context) error {
		return world(ctx).Browser.obs.ModelCallsReachedEndpoint()
	})
}

func (w *browserWorld) fixture(name string, brokenDelete bool) error {
	app, err := conformance.Fixture(name)
	if err != nil {
		return err
	}
	if brokenDelete {
		app = app.WithBrokenDelete()
	}
	if err := app.Start(); err != nil {
		return err
	}
	w.app = app
	if brokenDelete {
		w.runner = browsertest.NewBrokenDelete()
	} else {
		w.runner = browsertest.New()
	}
	return nil
}

func (w *browserWorld) runScripted(ctx context.Context, name string, rec conformance.Recording) error {
	feature, err := conformance.Feature(name)
	if err != nil {
		return err
	}
	w.feature = feature
	return w.startScripted(ctx, feature, rec)
}

func (w *browserWorld) startScripted(ctx context.Context, feature string, rec conformance.Recording) error {
	if w.runner == nil || w.app == nil {
		return fmt.Errorf("no browser runner under test")
	}
	// A scripted session records the DOM by default: a failing step's evidence
	// includes its rrweb timestamp (REQ-BROWSER-004), even when the scenario
	// does not name a recording.
	if !rec.RRWeb {
		rec = conformance.RRWebRecording()
	}
	obs, err := conformance.ScriptedSession(ctx, w.runner, w.app, feature, rec)
	if err != nil {
		return err
	}
	w.obs = obs
	return nil
}

func (w *browserWorld) startGoal(ctx context.Context) error {
	if w.runner == nil || w.app == nil {
		return fmt.Errorf("no browser runner under test")
	}
	if w.model == nil {
		return fmt.Errorf("no scripted model on the platform endpoint")
	}
	obs, err := conformance.GoalSession(ctx, w.runner, w.app, w.goal, w.budget, w.model)
	if err != nil {
		return err
	}
	w.obs = obs
	return nil
}

func recordingFor(name string) (conformance.Recording, error) {
	switch name {
	case "rrweb":
		return conformance.RRWebRecording(), nil
	default:
		return conformance.Recording{}, fmt.Errorf("unknown recording %q", name)
	}
}
