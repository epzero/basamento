package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// DOMEvent is one rrweb event.
type DOMEvent struct {
	Type      int             `json:"type"`
	Timestamp int64           `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// DOMRecording is a parsed rrweb artifact: the artifact entry, its raw bytes
// and its events.
type DOMRecording struct {
	Artifact Artifact
	Bytes    []byte
	Events   []DOMEvent
}

// Range is the recording's time range in milliseconds since session start.
func (r *DOMRecording) Range() (start, end int64) { return r.Artifact.StartMS, r.Artifact.EndMS }

// DOMRecording returns the session's rrweb artifact, parsed, and fails when
// the session has none.
func (o *Observation) DOMRecording(ctx context.Context) (*DOMRecording, error) {
	var artifact Artifact
	found := false
	for _, a := range o.Artifacts {
		if a.Kind == KindRRWeb {
			artifact, found = a, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("session has no %q artifact; artifacts are %v", KindRRWeb, artifactKinds(o.Artifacts))
	}
	b, err := o.ArtifactBytes(ctx, artifact)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Events []DOMEvent `json:"events"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return nil, fmt.Errorf("rrweb recording is not JSON: %w", err)
	}
	if len(envelope.Events) == 0 {
		return nil, fmt.Errorf("rrweb recording has no events")
	}
	if artifact.EndMS < artifact.StartMS {
		return nil, fmt.Errorf("rrweb recording range ends at %d before it starts at %d", artifact.EndMS, artifact.StartMS)
	}
	return &DOMRecording{Artifact: artifact, Bytes: b, Events: envelope.Events}, nil
}

// ProgressAndState checks that the session reported progress events and ended
// in want.
func (o *Observation) ProgressAndState(want State) error {
	if len(o.ProgressEvents()) == 0 {
		return fmt.Errorf("session reported no progress events")
	}
	if o.Result.State != want {
		return fmt.Errorf("session state is %q, want %q (reason %q, message %q)", o.Result.State, want, o.Result.Reason, o.Result.Message)
	}
	return nil
}

// EveryStepReported checks that every step of feature has a reported result.
func (o *Observation) EveryStepReported(feature string) error {
	steps := Steps(feature)
	if len(steps) == 0 {
		return fmt.Errorf("feature has no steps")
	}
	reported := map[string]StepResult{}
	for _, s := range o.StepResults() {
		reported[s.Step] = s
	}
	for _, step := range steps {
		if _, ok := reported[step]; !ok {
			return fmt.Errorf("step %q has no reported result; reported %v", step, reportedSteps(o.StepResults()))
		}
	}
	return nil
}

// ActionsWithinRecording checks that every action event falls inside the
// rrweb recording, both by the artifact's range and by its events.
func (o *Observation) ActionsWithinRecording(ctx context.Context) error {
	rec, err := o.DOMRecording(ctx)
	if err != nil {
		return err
	}
	actions := o.Actions()
	if len(actions) == 0 {
		return fmt.Errorf("session reported no action events to place in the recording")
	}
	start, end := rec.Range()
	minTS, maxTS := rec.Events[0].Timestamp, rec.Events[0].Timestamp
	for _, e := range rec.Events {
		if e.Timestamp < minTS {
			minTS = e.Timestamp
		}
		if e.Timestamp > maxTS {
			maxTS = e.Timestamp
		}
	}
	for _, a := range actions {
		if a.AtMS < start || a.AtMS > end {
			return fmt.Errorf("action %q at %dms is outside the recording range %d..%dms", a.Kind, a.AtMS, start, end)
		}
		if a.AtMS < minTS || a.AtMS > maxTS {
			return fmt.Errorf("action %q at %dms is outside the recording's events %d..%dms", a.Kind, a.AtMS, minTS, maxTS)
		}
	}
	return nil
}

// RecordingDoesNotContain checks that no artifact contains secret.
func (o *Observation) RecordingDoesNotContain(ctx context.Context, secret string) error {
	rec, err := o.DOMRecording(ctx)
	if err != nil {
		return err
	}
	if bytes.Contains(rec.Bytes, []byte(secret)) {
		return fmt.Errorf("rrweb recording contains the input value %q", secret)
	}
	return nil
}

// FailedStep returns the reported result of step and fails when it is not
// reported as failed with a message.
func (o *Observation) FailedStep(step string) (StepResult, error) {
	for _, s := range o.StepResults() {
		if s.Step != step {
			continue
		}
		if s.Passed {
			return s, fmt.Errorf("step %q passed, want failed", step)
		}
		if s.Message == "" {
			return s, fmt.Errorf("step %q failed with no message", step)
		}
		return s, nil
	}
	return StepResult{}, fmt.Errorf("step %q has no reported result; reported %v", step, reportedSteps(o.StepResults()))
}

// FailureEvidence checks that a failed step carries a screenshot and a
// timestamp inside the rrweb recording.
func (o *Observation) FailureEvidence(ctx context.Context, step string) error {
	s, err := o.FailedStep(step)
	if err != nil {
		return err
	}
	if s.ScreenshotKey == "" {
		return fmt.Errorf("step %q failed with no screenshot", step)
	}
	artifact, ok := findArtifact(o.Artifacts, s.ScreenshotKey)
	if !ok {
		return fmt.Errorf("screenshot %q of step %q is not an artifact; artifacts are %v", s.ScreenshotKey, step, artifactKinds(o.Artifacts))
	}
	if artifact.Kind != KindScreenshot {
		return fmt.Errorf("artifact %q of step %q is %q, want %q", s.ScreenshotKey, step, artifact.Kind, KindScreenshot)
	}
	if b, err := o.ArtifactBytes(ctx, artifact); err != nil {
		return err
	} else if len(b) == 0 {
		return fmt.Errorf("screenshot %q is empty", s.ScreenshotKey)
	}
	rec, err := o.DOMRecording(ctx)
	if err != nil {
		return err
	}
	start, end := rec.Range()
	if s.AtMS < start || s.AtMS > end {
		return fmt.Errorf("failure of step %q at %dms is outside the recording range %d..%dms", step, s.AtMS, start, end)
	}
	return nil
}

// BudgetExhausted checks that the session ended after steps actions with a
// failed result and reason budget_exceeded.
func (o *Observation) BudgetExhausted(steps int) error {
	if o.Result.State != StateFailed {
		return fmt.Errorf("session state is %q, want %q", o.Result.State, StateFailed)
	}
	if o.Result.Reason != ReasonBudgetExceeded {
		return fmt.Errorf("session reason is %q, want %q", o.Result.Reason, ReasonBudgetExceeded)
	}
	if got := len(o.Actions()); got != steps {
		return fmt.Errorf("session took %d actions, want %d", got, steps)
	}
	return nil
}

// ActionsHaveEvidence checks that every action names its target and carries a
// non-empty screenshot artifact.
func (o *Observation) ActionsHaveEvidence(ctx context.Context) error {
	actions := o.Actions()
	if len(actions) == 0 {
		return fmt.Errorf("session reported no actions")
	}
	for _, a := range actions {
		if a.Target == "" {
			return fmt.Errorf("action %q has no target", a.Kind)
		}
		if a.ScreenshotKey == "" {
			return fmt.Errorf("action %q on %q has no screenshot", a.Kind, a.Target)
		}
		artifact, ok := findArtifact(o.Artifacts, a.ScreenshotKey)
		if !ok {
			return fmt.Errorf("screenshot %q of action %q is not an artifact", a.ScreenshotKey, a.Kind)
		}
		if b, err := o.ArtifactBytes(ctx, artifact); err != nil {
			return err
		} else if len(b) == 0 {
			return fmt.Errorf("screenshot %q of action %q is empty", a.ScreenshotKey, a.Kind)
		}
	}
	return nil
}

// ModelCallsReachedEndpoint checks that the session made at least one model
// call and that every call reached the platform endpoint with the session's
// own token.
func (o *Observation) ModelCallsReachedEndpoint() error {
	if o.Request.ModelEndpoint == "" {
		return fmt.Errorf("session was given no model endpoint")
	}
	if o.Request.ModelToken == "" {
		return fmt.Errorf("session was given no model token")
	}
	if len(o.ModelCalls) == 0 {
		return fmt.Errorf("runner made no model calls")
	}
	for i, call := range o.ModelCalls {
		if call.Method != "POST" {
			return fmt.Errorf("model call %d used %s, want POST", i+1, call.Method)
		}
		if call.Path != ModelPath {
			return fmt.Errorf("model call %d reached %q, want %q", i+1, call.Path, ModelPath)
		}
		if want := "Bearer " + o.Request.ModelToken; call.Authorization != want {
			return fmt.Errorf("model call %d carried Authorization %q, want the session token", i+1, call.Authorization)
		}
	}
	return nil
}

// ReplayMatches checks that a replay reported the same actions, in the same
// order and at the same timestamps, as the recorded session.
func (o *Observation) ReplayMatches(recorded *Observation) error {
	want, got := recorded.Actions(), o.Actions()
	if len(got) != len(want) {
		return fmt.Errorf("replay took %d actions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].Target != want[i].Target || got[i].AtMS != want[i].AtMS {
			return fmt.Errorf("replay action %d is %q on %q at %dms, want %q on %q at %dms",
				i+1, got[i].Kind, got[i].Target, got[i].AtMS, want[i].Kind, want[i].Target, want[i].AtMS)
		}
	}
	return nil
}

// DescriptionSufficient checks that a runner describes at least one browser
// and one recorder.
func DescriptionSufficient(d Description) error {
	if len(d.Browsers) == 0 {
		return fmt.Errorf("runner describes no browsers")
	}
	if len(d.Recorders) == 0 {
		return fmt.Errorf("runner describes no recorders")
	}
	return nil
}

func findArtifact(artifacts []Artifact, key string) (Artifact, bool) {
	for _, a := range artifacts {
		if a.BlobKey == key {
			return a, true
		}
	}
	return Artifact{}, false
}

func artifactKinds(artifacts []Artifact) []string {
	kinds := make([]string, 0, len(artifacts))
	for _, a := range artifacts {
		kinds = append(kinds, a.Kind+":"+a.BlobKey)
	}
	return kinds
}

func reportedSteps(steps []StepResult) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Step)
	}
	return out
}

func marshalActions(actions []Action) ([]byte, error) {
	if actions == nil {
		actions = []Action{}
	}
	return json.Marshal(actions)
}

// DecodeActions reads the JSON form of a recorded action sequence.
func DecodeActions(b []byte) ([]Action, error) {
	var actions []Action
	if err := json.Unmarshal(b, &actions); err != nil {
		return nil, fmt.Errorf("recorded actions: %w", err)
	}
	return actions, nil
}
