package conformance

import "context"

// Mode selects how a runner drives the browser; it mirrors SessionMode.
type Mode string

const (
	// ModeScripted executes the scenarios of a feature.
	ModeScripted Mode = "scripted"
	// ModeGoal pursues a goal within a budget.
	ModeGoal Mode = "goal"
)

// Recording selects the evidence a session captures; it mirrors Recording.
// Input values are masked unless their selector appears in UnmaskSelectors.
type Recording struct {
	RRWeb           bool     `json:"rrweb,omitempty"`
	Video           bool     `json:"video,omitempty"`
	Trace           bool     `json:"trace,omitempty"`
	HAR             bool     `json:"har,omitempty"`
	Console         bool     `json:"console,omitempty"`
	Accessibility   bool     `json:"accessibility,omitempty"`
	UnmaskSelectors []string `json:"unmaskSelectors,omitempty"`
	BlockSelectors  []string `json:"blockSelectors,omitempty"`
}

// RRWebRecording asks for the rrweb DOM recording and no other evidence.
func RRWebRecording() Recording { return Recording{RRWeb: true} }

// Budget bounds a session; it mirrors Budget.
type Budget struct {
	MaxSteps      int32 `json:"maxSteps,omitempty"`
	MaxDurationMS int64 `json:"maxDurationMs,omitempty"`
	MaxCostMPts   int64 `json:"maxCostMpts,omitempty"`
}

// Target is the app under test; it mirrors Target.
type Target struct {
	BaseURL     string `json:"baseUrl,omitempty"`
	AuthFixture string `json:"authFixture,omitempty"`
	Device      string `json:"device,omitempty"`
	Locale      string `json:"locale,omitempty"`
}

// Replay is a recorded action sequence; it mirrors Replay.
type Replay struct {
	Actions []byte `json:"actions,omitempty"`
	BlobKey string `json:"blobKey,omitempty"`
}

// SessionSpec is the params of a "browser.session" invocation, in its JSON
// form; it mirrors SessionSpec.
type SessionSpec struct {
	Mode         Mode      `json:"mode,omitempty"`
	Target       Target    `json:"target,omitempty"`
	Feature      string    `json:"feature,omitempty"`
	Goal         string    `json:"goal,omitempty"`
	Instructions string    `json:"instructions,omitempty"`
	Budget       Budget    `json:"budget,omitempty"`
	Recording    Recording `json:"recording,omitempty"`
	Replay       *Replay   `json:"replay,omitempty"`
}

// Action is one UI action a runner performed; it mirrors Action.
type Action struct {
	Kind          string `json:"kind,omitempty"`
	Target        string `json:"target,omitempty"`
	AtMS          int64  `json:"atMs,omitempty"`
	ScreenshotKey string `json:"screenshotKey,omitempty"`
}

// StepResult is the outcome of one scripted step; it mirrors StepResult.
// Step is the full step line, keyword included, as it appears in the feature.
type StepResult struct {
	Scenario      string `json:"scenario,omitempty"`
	Step          string `json:"step,omitempty"`
	Passed        bool   `json:"passed,omitempty"`
	Message       string `json:"message,omitempty"`
	AtMS          int64  `json:"atMs,omitempty"`
	ScreenshotKey string `json:"screenshotKey,omitempty"`
}

// EventKind is the kind of one reported session event. A progress event has
// text and the other two kinds carry their typed payload; together they are
// the Go form of an invoke StreamEventsResponse plus a browser SessionEvent.
type EventKind string

const (
	// EventProgress reports session progress, such as navigation.
	EventProgress EventKind = "progress"
	// EventAction reports one UI action.
	EventAction EventKind = "action"
	// EventStep reports one scripted step's result.
	EventStep EventKind = "step"
)

// Event is one event of a session, in order.
type Event struct {
	Seq    int64       `json:"seq"`
	Kind   EventKind   `json:"kind"`
	Text   string      `json:"text,omitempty"`
	Action *Action     `json:"action,omitempty"`
	Step   *StepResult `json:"step,omitempty"`
}

// State is the state of a session; it mirrors invoke State.
type State string

const (
	// StateRunning means the session has not ended.
	StateRunning State = "running"
	// StateSucceeded means the session reached its end without a failure.
	StateSucceeded State = "succeeded"
	// StateFailed means the session ended with a failure.
	StateFailed State = "failed"
	// StateCancelled means the session was cancelled.
	StateCancelled State = "cancelled"
)

// Failure reasons a session reports; they match SessionFailure.reason.
const (
	// ReasonBudgetExceeded means the session spent its budget of steps.
	ReasonBudgetExceeded = "budget_exceeded"
	// ReasonStepFailed means a scripted step failed.
	ReasonStepFailed = "step_failed"
	// ReasonError means the runner could not continue.
	ReasonError = "error"
)

// Result is a session's state and, once ended, why it ended.
type Result struct {
	State   State  `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Artifact is one evidence file; it mirrors Artifact.
type Artifact struct {
	Kind    string `json:"kind,omitempty"`
	BlobKey string `json:"blobKey,omitempty"`
	MIME    string `json:"mime,omitempty"`
	StartMS int64  `json:"startMs,omitempty"`
	EndMS   int64  `json:"endMs,omitempty"`
}

// Artifact kinds the contract names.
const (
	// KindRRWeb is the rrweb DOM recording.
	KindRRWeb = "rrweb"
	// KindScreenshot is a screenshot of one action or failure.
	KindScreenshot = "screenshot"
)

// Description is what a runner supports; it mirrors DescribeResponse.
type Description struct {
	Browsers  []string `json:"browsers,omitempty"`
	Devices   []string `json:"devices,omitempty"`
	Recorders []string `json:"recorders,omitempty"`
}

// Handle identifies a started session; its ID is the session id.
type Handle struct {
	ID string `json:"id"`
}

// StartRequest is one Start call. The model endpoint and token travel beside
// the spec, as request headers do on the wire, because params are recorded
// with the run and the token must not be.
type StartRequest struct {
	Spec          SessionSpec
	ModelEndpoint string
	ModelToken    string
	ClientRef     string
}

// Runner is the contract a browser runner implements: the invocation
// contract (Describe, Start, StreamEvents, GetResult, Cancel) plus what is
// specific to browsers (GetArtifacts, OpenArtifact). A network
// implementation wraps InvocationService and BrowserRunnerService; a test
// implementation is deterministic and in-process.
type Runner interface {
	// Describe reports the browsers, devices and recorders the runner offers.
	Describe(ctx context.Context) (Description, error)
	// Start begins a session and returns its handle.
	Start(ctx context.Context, req StartRequest) (Handle, error)
	// StreamEvents returns the events after afterSeq, in order.
	StreamEvents(ctx context.Context, handle Handle, afterSeq int64) ([]Event, error)
	// GetResult returns the session's state and, once ended, why it ended.
	GetResult(ctx context.Context, handle Handle) (Result, error)
	// Cancel requests cancellation; the result reports the final state.
	Cancel(ctx context.Context, handle Handle) error
	// GetArtifacts lists a session's evidence.
	GetArtifacts(ctx context.Context, handle Handle) ([]Artifact, error)
	// OpenArtifact reads one artifact's content, as a scoped blob URL would.
	OpenArtifact(ctx context.Context, handle Handle, blobKey string) ([]byte, error)
}
