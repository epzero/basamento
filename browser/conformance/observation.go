package conformance

import (
	"context"
	"fmt"
)

// DefaultBudgetSteps is the step budget a goal-driven session gets when its
// spec names none.
const DefaultBudgetSteps = 10

// Observation is one session, as a caller sees it: its handle, its events in
// order, its result, its artifacts and the model calls it made.
type Observation struct {
	// Request is the start request the session was given.
	Request StartRequest
	// Handle is the started session's handle.
	Handle Handle
	// Events are the session's events, in order.
	Events []Event
	// Result is the session's final state.
	Result Result
	// Artifacts is the session's evidence.
	Artifacts []Artifact
	// ModelCalls is the calls the session made to its model endpoint.
	ModelCalls []ModelCall

	runner Runner
}

// ScriptedSession starts a scripted session for a feature against an app and
// follows it to its end. recording may be the zero value.
func ScriptedSession(ctx context.Context, r Runner, app *App, feature string, recording Recording) (*Observation, error) {
	req := StartRequest{
		Spec: SessionSpec{
			Mode:      ModeScripted,
			Target:    Target{BaseURL: app.URL(), Device: "desktop"},
			Feature:   feature,
			Recording: recording,
		},
	}
	return Observe(ctx, r, req)
}

// GoalSession starts a goal-driven session against an app, with model as the
// platform endpoint, and follows it to its end. A zero budget gets
// DefaultBudgetSteps.
func GoalSession(ctx context.Context, r Runner, app *App, goal string, budget Budget, model *ScriptedModel) (*Observation, error) {
	if budget.MaxSteps == 0 {
		budget.MaxSteps = DefaultBudgetSteps
	}
	req := StartRequest{
		Spec: SessionSpec{
			Mode:   ModeGoal,
			Target: Target{BaseURL: app.URL(), Device: "desktop"},
			Goal:   goal,
			Budget: budget,
		},
		ModelEndpoint: model.Endpoint(),
		ModelToken:    model.Token(),
	}
	obs, err := Observe(ctx, r, req)
	if obs != nil {
		obs.ModelCalls = model.Calls()
	}
	return obs, err
}

// ReplaySession re-executes a recorded action sequence against an app and
// returns what the runner reported.
func ReplaySession(ctx context.Context, r Runner, app *App, actions []byte) (*Observation, error) {
	req := StartRequest{
		Spec: SessionSpec{
			Mode:   ModeGoal,
			Target: Target{BaseURL: app.URL(), Device: "desktop"},
			Replay: &Replay{Actions: actions},
		},
	}
	return Observe(ctx, r, req)
}

// Observe starts a session, follows it and reads its result and artifacts.
func Observe(ctx context.Context, r Runner, req StartRequest) (*Observation, error) {
	handle, err := r.Start(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}
	events, err := r.StreamEvents(ctx, handle, 0)
	if err != nil {
		return nil, fmt.Errorf("stream events: %w", err)
	}
	result, err := r.GetResult(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("get result: %w", err)
	}
	artifacts, err := r.GetArtifacts(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("get artifacts: %w", err)
	}
	return &Observation{
		Request:   req,
		Handle:    handle,
		Events:    events,
		Result:    result,
		Artifacts: artifacts,
		runner:    r,
	}, nil
}

// ArtifactBytes reads one artifact's content.
func (o *Observation) ArtifactBytes(ctx context.Context, a Artifact) ([]byte, error) {
	b, err := o.runner.OpenArtifact(ctx, o.Handle, a.BlobKey)
	if err != nil {
		return nil, fmt.Errorf("open artifact %q: %w", a.BlobKey, err)
	}
	return b, nil
}

// Actions returns the session's action events in order.
func (o *Observation) Actions() []Action {
	var actions []Action
	for _, e := range o.Events {
		if e.Kind == EventAction && e.Action != nil {
			actions = append(actions, *e.Action)
		}
	}
	return actions
}

// StepResults returns the session's step events in order.
func (o *Observation) StepResults() []StepResult {
	var steps []StepResult
	for _, e := range o.Events {
		if e.Kind == EventStep && e.Step != nil {
			steps = append(steps, *e.Step)
		}
	}
	return steps
}

// ProgressEvents returns the session's progress events in order.
func (o *Observation) ProgressEvents() []string {
	var texts []string
	for _, e := range o.Events {
		if e.Kind == EventProgress {
			texts = append(texts, e.Text)
		}
	}
	return texts
}

// RecordedActions returns the session's action sequence in the JSON form a
// Replay takes.
func (o *Observation) RecordedActions() ([]byte, error) {
	b, err := marshalActions(o.Actions())
	if err != nil {
		return nil, err
	}
	return b, nil
}
