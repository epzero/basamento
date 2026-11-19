// Package browsertest holds the simulated browser runner the conformance
// suite and the BDD steps run against. It is a test double and test-only: it
// imports only the conformance contract, drives the reference fixture apps
// over loopback HTTP and answers models with a scripted endpoint.
//
// It is deterministic: event timestamps come from a counter, never the clock,
// and every session produces the same events, artifacts and model calls.
package browsertest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/epzero/basamento/browser/conformance"
)

// Runner is a simulated browser runner that passes the conformance suite.
type Runner struct {
	brokenDelete bool

	mu       sync.Mutex
	next     int
	sessions map[string]*session
	client   *http.Client
}

type session struct {
	request  conformance.StartRequest
	handle   conformance.Handle
	events   []conformance.Event
	result   conformance.Result
	blobs    map[string][]byte
	artifact []conformance.Artifact
	dom      []conformance.DOMEvent
	at       int64
	shots    int
}

// New returns a simulated runner.
func New() *Runner {
	return &Runner{
		sessions: map[string]*session{},
		client:   &http.Client{},
	}
}

// NewBrokenDelete returns a simulated runner whose todo fixture app has a
// broken delete button.
func NewBrokenDelete() *Runner {
	r := New()
	r.brokenDelete = true
	return r
}

// Describe reports what the simulated runner offers.
func (r *Runner) Describe(context.Context) (conformance.Description, error) {
	return conformance.Description{
		Browsers:  []string{"chromium"},
		Devices:   []string{"desktop"},
		Recorders: []string{"rrweb"},
	}, nil
}

// Start begins a session and runs it to its end synchronously.
func (r *Runner) Start(ctx context.Context, req conformance.StartRequest) (conformance.Handle, error) {
	if req.Spec.Target.BaseURL == "" {
		return conformance.Handle{}, fmt.Errorf("session has no target base URL")
	}
	r.mu.Lock()
	r.next++
	handle := conformance.Handle{ID: fmt.Sprintf("sess-%d", r.next)}
	r.mu.Unlock()

	s := &session{
		request: req,
		handle:  handle,
		blobs:   map[string][]byte{},
		at:      100,
	}
	if err := r.navigate(ctx, s); err != nil {
		s.result = conformance.Result{State: conformance.StateFailed, Reason: conformance.ReasonError, Message: err.Error()}
		s.finalize()
		r.save(s)
		return handle, nil
	}
	switch {
	case req.Spec.Replay != nil:
		if err := r.replay(s); err != nil {
			return conformance.Handle{}, err
		}
	case req.Spec.Mode == conformance.ModeScripted:
		r.scripted(s)
	case req.Spec.Mode == conformance.ModeGoal:
		r.goal(ctx, s)
	default:
		return conformance.Handle{}, fmt.Errorf("session mode %q is not supported", req.Spec.Mode)
	}
	s.progress("session finished")
	s.finalize()
	r.save(s)
	return handle, nil
}

// StreamEvents returns the events after afterSeq.
func (r *Runner) StreamEvents(_ context.Context, handle conformance.Handle, afterSeq int64) ([]conformance.Event, error) {
	s, err := r.session(handle)
	if err != nil {
		return nil, err
	}
	var out []conformance.Event
	for _, e := range s.events {
		if e.Seq > afterSeq {
			out = append(out, e)
		}
	}
	return out, nil
}

// GetResult returns the session's result.
func (r *Runner) GetResult(_ context.Context, handle conformance.Handle) (conformance.Result, error) {
	s, err := r.session(handle)
	if err != nil {
		return conformance.Result{}, err
	}
	return s.result, nil
}

// Cancel marks a session cancelled.
func (r *Runner) Cancel(_ context.Context, handle conformance.Handle) error {
	s, err := r.session(handle)
	if err != nil {
		return err
	}
	if s.result.State == conformance.StateRunning || s.result.State == "" {
		s.result = conformance.Result{State: conformance.StateCancelled}
	}
	return nil
}

// GetArtifacts returns the session's evidence.
func (r *Runner) GetArtifacts(_ context.Context, handle conformance.Handle) ([]conformance.Artifact, error) {
	s, err := r.session(handle)
	if err != nil {
		return nil, err
	}
	return append([]conformance.Artifact(nil), s.artifact...), nil
}

// OpenArtifact reads one artifact's content.
func (r *Runner) OpenArtifact(_ context.Context, handle conformance.Handle, blobKey string) ([]byte, error) {
	s, err := r.session(handle)
	if err != nil {
		return nil, err
	}
	b, ok := s.blobs[blobKey]
	if !ok {
		return nil, fmt.Errorf("no artifact %q in session %q", blobKey, handle.ID)
	}
	return append([]byte(nil), b...), nil
}

func (r *Runner) session(handle conformance.Handle) (*session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[handle.ID]
	if !ok {
		return nil, fmt.Errorf("unknown session %q", handle.ID)
	}
	return s, nil
}

func (r *Runner) save(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.handle.ID] = s
}

func (r *Runner) navigate(ctx context.Context, s *session) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.request.Spec.Target.BaseURL, nil)
	if err != nil {
		return fmt.Errorf("navigation request: %w", err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("navigate to %s: %w", s.request.Spec.Target.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("navigate to %s: status %d", s.request.Spec.Target.BaseURL, resp.StatusCode)
	}
	if len(body) == 0 {
		return fmt.Errorf("navigate to %s: empty page", s.request.Spec.Target.BaseURL)
	}
	s.progress("navigated to " + s.request.Spec.Target.BaseURL)
	return nil
}

func (s *session) tick() int64 {
	at := s.at
	s.at += 100
	return at
}

func (s *session) progress(text string) {
	s.events = append(s.events, conformance.Event{
		Seq:  int64(len(s.events) + 1),
		Kind: conformance.EventProgress,
		Text: text,
	})
}

// action reports one UI action and records it, with text, in the DOM
// recording. text is what a recorder would capture for it; a typed value is
// masked before it reaches text unless the session unmasks the field.
func (s *session) action(a conformance.Action, text string) {
	a.AtMS = s.tick()
	a.ScreenshotKey = s.screenshot(a.Kind, a.Target)
	s.events = append(s.events, conformance.Event{
		Seq:    int64(len(s.events) + 1),
		Kind:   conformance.EventAction,
		Action: &a,
	})
	s.domEvent(a.AtMS, text)
}

func (s *session) step(result conformance.StepResult) {
	result.AtMS = s.tick()
	if result.Passed {
		result.ScreenshotKey = s.screenshot("step", result.Step)
	} else {
		result.ScreenshotKey = s.screenshot("failure", result.Step)
	}
	s.events = append(s.events, conformance.Event{
		Seq:  int64(len(s.events) + 1),
		Kind: conformance.EventStep,
		Step: &result,
	})
	if !result.Passed {
		s.domEvent(result.AtMS, result.Step)
	}
}

func (s *session) screenshot(kind, target string) string {
	s.shots++
	key := fmt.Sprintf("screenshots/%s-%d.png", s.handle.ID, s.shots)
	s.blobs[key] = []byte(fmt.Sprintf("PNG %s %s", kind, target))
	s.artifact = append(s.artifact, conformance.Artifact{
		Kind:    conformance.KindScreenshot,
		BlobKey: key,
		MIME:    "image/png",
	})
	return key
}

func (s *session) domEvent(at int64, text string) {
	data, _ := json.Marshal(map[string]string{"text": text})
	s.dom = append(s.dom, conformance.DOMEvent{Type: 3, Timestamp: at, Data: data})
}

// scripted runs the steps of a feature. A step that interacts with the page
// also reports an action. The first failed step ends the session.
func (r *Runner) scripted(s *session) {
	feature := s.request.Spec.Feature
	scenario := conformance.ScenarioName(feature)
	s.progress("session started")
	for _, step := range conformance.Steps(feature) {
		action, interacts := actionFor(step, s.request.Spec)
		if interacts {
			s.action(action, domText(step, action, s.request.Spec.Recording))
		}
		failed := r.brokenDelete && containsDelete(step)
		result := conformance.StepResult{Scenario: scenario, Step: step, Passed: !failed}
		if failed {
			result.Message = "the delete button did not respond"
		}
		s.step(result)
		if failed {
			s.result = conformance.Result{
				State:   conformance.StateFailed,
				Reason:  conformance.ReasonStepFailed,
				Message: result.Message,
			}
			return
		}
	}
	s.result = conformance.Result{State: conformance.StateSucceeded}
}

// goal pursues a goal until the model declares it reached or the step budget
// is spent.
func (r *Runner) goal(ctx context.Context, s *session) {
	budget := s.request.Spec.Budget.MaxSteps
	if budget <= 0 {
		budget = conformance.DefaultBudgetSteps
	}
	s.progress("session started")
	for step := int32(0); step < budget; step++ {
		content, action, err := r.ask(ctx, s)
		if err != nil {
			s.result = conformance.Result{State: conformance.StateFailed, Reason: conformance.ReasonError, Message: err.Error()}
			return
		}
		if content == conformance.GoalReached {
			s.result = conformance.Result{State: conformance.StateSucceeded}
			return
		}
		s.action(conformance.Action{Kind: action.Kind, Target: action.Target}, action.Target)
	}
	s.result = conformance.Result{
		State:   conformance.StateFailed,
		Reason:  conformance.ReasonBudgetExceeded,
		Message: fmt.Sprintf("budget of %d steps spent", budget),
	}
}

func (r *Runner) ask(ctx context.Context, s *session) (string, conformance.ModelAction, error) {
	if s.request.ModelEndpoint == "" || s.request.ModelToken == "" {
		return "", conformance.ModelAction{}, fmt.Errorf("session has no model endpoint or token")
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "conformance",
		"messages": []any{map[string]any{"role": "user", "content": s.request.Spec.Goal}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.request.ModelEndpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", conformance.ModelAction{}, fmt.Errorf("model request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.request.ModelToken)
	req.Header.Set("Basamento-Session", s.handle.ID)
	resp, err := r.client.Do(req)
	if err != nil {
		return "", conformance.ModelAction{}, fmt.Errorf("model call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	reply, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", conformance.ModelAction{}, fmt.Errorf("model call: status %d: %s", resp.StatusCode, string(reply))
	}
	content, action, err := conformance.ParseModelReply(reply)
	if err != nil {
		return "", conformance.ModelAction{}, err
	}
	return content, action, nil
}

// replay re-executes recorded actions at their recorded timestamps.
func (r *Runner) replay(s *session) error {
	actions, err := conformance.DecodeActions(s.request.Spec.Replay.Actions)
	if err != nil {
		return err
	}
	s.progress("replay started")
	for _, a := range actions {
		a.ScreenshotKey = s.screenshot("replay", a.Target)
		s.events = append(s.events, conformance.Event{
			Seq:    int64(len(s.events) + 1),
			Kind:   conformance.EventAction,
			Action: &a,
		})
		s.domEvent(a.AtMS, a.Target)
	}
	s.result = conformance.Result{State: conformance.StateSucceeded}
	return nil
}

// finalize writes the rrweb artifact when the session asked for one. Its
// range covers every reported event.
func (s *session) finalize() {
	if !s.request.Spec.Recording.RRWeb {
		return
	}
	end := int64(0)
	for _, e := range s.dom {
		if e.Timestamp > end {
			end = e.Timestamp
		}
	}
	for _, e := range s.events {
		if at := atOf(e); at > end {
			end = at
		}
	}
	envelope, _ := json.Marshal(map[string]any{"events": s.dom})
	key := fmt.Sprintf("recordings/%s.json", s.handle.ID)
	s.blobs[key] = envelope
	s.artifact = append(s.artifact, conformance.Artifact{
		Kind:    conformance.KindRRWeb,
		BlobKey: key,
		MIME:    "application/json",
		StartMS: 0,
		EndMS:   end + 100,
	})
}

func atOf(e conformance.Event) int64 {
	switch e.Kind {
	case conformance.EventAction:
		if e.Action != nil {
			return e.Action.AtMS
		}
	case conformance.EventStep:
		if e.Step != nil {
			return e.Step.AtMS
		}
	}
	return 0
}

// actionFor turns one interaction step into a UI action, and reports whether
// the step interacts at all.
func actionFor(step string, spec conformance.SessionSpec) (conformance.Action, bool) {
	switch {
	case containsDelete(step):
		return conformance.Action{Kind: "click", Target: "#items .delete"}, true
	case containsWord(step, "types"):
		return conformance.Action{Kind: "type", Target: fieldTarget(step)}, true
	case containsWord(step, "clicks"):
		return conformance.Action{Kind: "click", Target: "#add"}, true
	case containsWord(step, "opens"):
		return conformance.Action{Kind: "navigate", Target: spec.Target.BaseURL}, true
	}
	return conformance.Action{}, false
}

// domText is what a recorder captures for one action. A typed value is
// replaced by a mask unless the session unmasks the field's selector.
func domText(step string, action conformance.Action, rec conformance.Recording) string {
	if action.Kind != "type" {
		return action.Target
	}
	value, ok := typedValue(step)
	if !ok {
		return action.Target
	}
	for _, selector := range rec.UnmaskSelectors {
		if selector == action.Target {
			return value
		}
	}
	return "••••••"
}

// typedValue reads the value of a "types "…"" step.
func typedValue(step string) (string, bool) {
	open := strings.IndexByte(step, '"')
	if open < 0 {
		return "", false
	}
	rest := step[open+1:]
	closeAt := strings.IndexByte(rest, '"')
	if closeAt < 0 {
		return "", false
	}
	return rest[:closeAt], true
}

// fieldTarget names the field a type step fills.
func fieldTarget(step string) string {
	lower := strings.ToLower(step)
	switch {
	case strings.Contains(lower, "password"):
		return "#password"
	case strings.Contains(lower, "new item"):
		return "#new-item"
	case strings.Contains(lower, "item field"):
		return "#new-item"
	default:
		return "#input"
	}
}

func containsDelete(s string) bool { return containsWord(s, "delete") }

func containsWord(s, word string) bool { return strings.Contains(strings.ToLower(s), word) }
