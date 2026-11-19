package conformance_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/epzero/basamento/browser/browsertest"
	"github.com/epzero/basamento/browser/conformance"
)

func startApp(t *testing.T, name string) *conformance.App {
	t.Helper()
	app, err := conformance.Fixture(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app
}

func feature(t *testing.T, name string) string {
	t.Helper()
	text, err := conformance.Feature(name)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestSimulatedRunnerPassesScriptedSession(t *testing.T) {
	app := startApp(t, conformance.AppTodo)
	runner := browsertest.New()

	d, err := runner.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := conformance.DescriptionSufficient(d); err != nil {
		t.Fatal(err)
	}

	add := feature(t, "todo/add-item.feature")
	obs, err := conformance.ScriptedSession(context.Background(), runner, app, add, conformance.RRWebRecording())
	if err != nil {
		t.Fatal(err)
	}
	if err := obs.ProgressAndState(conformance.StateSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := obs.EveryStepReported(add); err != nil {
		t.Fatal(err)
	}
	if err := obs.ActionsWithinRecording(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSimulatedRunnerPassesFailingStep(t *testing.T) {
	app := startApp(t, conformance.AppTodo)
	runner := browsertest.NewBrokenDelete()

	del := feature(t, "todo/delete-item.feature")
	obs, err := conformance.ScriptedSession(context.Background(), runner, app, del, conformance.RRWebRecording())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := obs.FailedStep("When ana deletes the item"); err != nil {
		t.Fatal(err)
	}
	if err := obs.FailureEvidence(context.Background(), "When ana deletes the item"); err != nil {
		t.Fatal(err)
	}
	if obs.Result.State != conformance.StateFailed || obs.Result.Reason != conformance.ReasonStepFailed {
		t.Fatalf("result = %+v, want failed/step_failed", obs.Result)
	}
}

func TestInputValuesAreMasked(t *testing.T) {
	app := startApp(t, conformance.AppLogin)
	runner := browsertest.New()

	const value = "s3cret"
	login := "Feature: Sign in\n  Scenario: ana signs in\n    When ana types \"" + value + "\" into the password field\n"

	obs, err := conformance.ScriptedSession(context.Background(), runner, app, login, conformance.RRWebRecording())
	if err != nil {
		t.Fatal(err)
	}
	if err := obs.RecordingDoesNotContain(context.Background(), value); err != nil {
		t.Fatal(err)
	}

	// An explicitly unmasked field is the one case the contract allows.
	unmasked := conformance.RRWebRecording()
	unmasked.UnmaskSelectors = []string{"#password"}
	obs, err = conformance.ScriptedSession(context.Background(), runner, app, login, unmasked)
	if err != nil {
		t.Fatal(err)
	}
	if err := obs.RecordingDoesNotContain(context.Background(), value); err == nil {
		t.Fatal("an unmasked field recorded no value, want it recorded")
	}
}

func TestGoalSessionStopsAtBudget(t *testing.T) {
	app := startApp(t, conformance.AppTodo)
	runner := browsertest.New()
	model := conformance.NewScriptedModel("session-token")
	t.Cleanup(model.Close)

	obs, err := conformance.GoalSession(context.Background(), runner, app, "add two items",
		conformance.Budget{MaxSteps: 5}, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := obs.BudgetExhausted(5); err != nil {
		t.Fatal(err)
	}
	if err := obs.ActionsHaveEvidence(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := obs.ModelCallsReachedEndpoint(); err != nil {
		t.Fatal(err)
	}
	if got := len(obs.ModelCalls); got != 5 {
		t.Fatalf("model calls = %d, want 5", got)
	}
}

func TestGoalSessionSucceedsWhenTheModelReachesTheGoal(t *testing.T) {
	app := startApp(t, conformance.AppTodo)
	runner := browsertest.New()
	model := conformance.NewScriptedModel("session-token")
	t.Cleanup(model.Close)
	model.ReachGoal()

	obs, err := conformance.GoalSession(context.Background(), runner, app, "add two items",
		conformance.Budget{MaxSteps: 5}, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := obs.ProgressAndState(conformance.StateSucceeded); err != nil {
		t.Fatal(err)
	}
	if len(obs.Actions()) != 0 {
		t.Fatalf("actions = %d, want 0 before the goal was declared", len(obs.Actions()))
	}
}

func TestReplayReproducesTheRecordedActions(t *testing.T) {
	app := startApp(t, conformance.AppTodo)
	runner := browsertest.New()
	model := conformance.NewScriptedModel("session-token")
	t.Cleanup(model.Close)

	recorded, err := conformance.GoalSession(context.Background(), runner, app, "add two items",
		conformance.Budget{MaxSteps: 3}, model)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := recorded.RecordedActions()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := conformance.ReplaySession(context.Background(), runner, app, actions)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayed.ReplayMatches(recorded); err != nil {
		t.Fatal(err)
	}
	if got := len(replayed.ModelCalls); got != 0 {
		t.Fatalf("replay made %d model calls, want 0", got)
	}
}

// leakingRunner is a runner that records a secret anyway, to prove the
// masking check fails rather than passing everything.
type leakingRunner struct {
	conformance.Runner
	secret string
}

func (l leakingRunner) OpenArtifact(ctx context.Context, handle conformance.Handle, key string) ([]byte, error) {
	b, err := l.Runner.OpenArtifact(ctx, handle, key)
	if err != nil {
		return nil, err
	}
	if strings.Contains(key, "recordings/") {
		b = append(b, []byte(`{"events":[],"leak":"`+l.secret+`"}`)...)
	}
	return b, nil
}

func TestMaskingCheckCatchesARecordedSecret(t *testing.T) {
	app := startApp(t, conformance.AppLogin)
	runner := leakingRunner{Runner: browsertest.New(), secret: "s3cret"}

	login := "Feature: Sign in\n  Scenario: ana signs in\n    When ana types \"s3cret\" into the password field\n"
	obs, err := conformance.ScriptedSession(context.Background(), runner, app, login, conformance.RRWebRecording())
	if err != nil {
		t.Fatal(err)
	}
	if err := obs.RecordingDoesNotContain(context.Background(), "s3cret"); err == nil {
		t.Fatal("check passed while the recording held the secret")
	}
}

func TestChecksRejectWrongStates(t *testing.T) {
	failed := &conformance.Observation{Result: conformance.Result{State: conformance.StateFailed}}
	if err := failed.ProgressAndState(conformance.StateSucceeded); err == nil {
		t.Fatal("ProgressAndState passed for a failed session")
	}
	if err := failed.BudgetExhausted(3); err == nil {
		t.Fatal("BudgetExhausted passed without the budget reason")
	}
	empty := &conformance.Observation{Result: conformance.Result{State: conformance.StateSucceeded}}
	if err := empty.ProgressAndState(conformance.StateSucceeded); err == nil {
		t.Fatal("ProgressAndState passed without progress events")
	}
}

func TestSessionsAreIsolated(t *testing.T) {
	app := startApp(t, conformance.AppTodo)
	runner := browsertest.New()
	add := feature(t, "todo/add-item.feature")
	ctx := context.Background()

	first, err := conformance.ScriptedSession(ctx, runner, app, add, conformance.RRWebRecording())
	if err != nil {
		t.Fatal(err)
	}
	second, err := conformance.ScriptedSession(ctx, runner, app, add, conformance.RRWebRecording())
	if err != nil {
		t.Fatal(err)
	}
	if first.Handle.ID == second.Handle.ID {
		t.Fatalf("both sessions got handle %q", first.Handle.ID)
	}
	for _, artifact := range first.Artifacts {
		if _, err := runner.OpenArtifact(ctx, second.Handle, artifact.BlobKey); err == nil {
			t.Fatalf("session %q read artifact %q of session %q", second.Handle.ID, artifact.BlobKey, first.Handle.ID)
		}
	}
	if got := len(second.Events); got == 0 {
		t.Fatal("second session reported no events")
	}
}

func TestFixtureMetadata(t *testing.T) {
	for _, name := range []string{"todo/add-item.feature", "todo/delete-item.feature"} {
		text := feature(t, name)
		if conformance.ScenarioName(text) == "" {
			t.Fatalf("%s has no scenario", name)
		}
		if len(conformance.Steps(text)) < 3 {
			t.Fatalf("%s has %d steps", name, len(conformance.Steps(text)))
		}
	}
	if _, err := conformance.Fixture("nope"); err == nil {
		t.Fatal("Fixture accepted an unknown app")
	}
	if _, err := conformance.Feature("nope.feature"); err == nil {
		t.Fatal("Feature accepted an unknown name")
	}
}

func TestSimulatorIsDeterministic(t *testing.T) {
	app := startApp(t, conformance.AppTodo)
	featureText := feature(t, "todo/add-item.feature")

	run := func() ([]byte, []byte) {
		runner := browsertest.New()
		obs, err := conformance.ScriptedSession(context.Background(), runner, app, featureText, conformance.RRWebRecording())
		if err != nil {
			t.Fatal(err)
		}
		rec, err := obs.DOMRecording(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		events, err := json.Marshal(obs.Events)
		if err != nil {
			t.Fatal(err)
		}
		return events, rec.Bytes
	}
	firstEvents, firstRecording := run()
	secondEvents, secondRecording := run()
	if string(firstEvents) != string(secondEvents) {
		t.Fatalf("events differ between runs:\n%s\n%s", firstEvents, secondEvents)
	}
	if string(firstRecording) != string(secondRecording) {
		t.Fatalf("recordings differ between runs:\n%s\n%s", firstRecording, secondRecording)
	}
}
