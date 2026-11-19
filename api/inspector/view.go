package inspector

import (
	"encoding/json"
	"html/template"

	"github.com/epzero/basamento/core"
)

// Rejection is the API's answer to a payload a gate did not accept. The gate
// stays open (REQ-INSP-007).
type Rejection struct {
	Code string
}

// listView is the runs page.
type listView struct {
	Prefix string
	Runs   []RunSummary
	JSON   template.JS
}

// runView is the run detail page.
type runView struct {
	Prefix    string
	Run       Run
	Events    []Event
	Recording recordingView
	Rejection *Rejection
	JSON      template.JS
}

// deniedView is what a principal without the permission sees: no run data at
// all (REQ-INSP-013).
type deniedView struct {
	Prefix string
	Code   string
}

// recordingView is the recording section of the detail page.
type recordingView struct {
	State   string
	Count   int
	Stopped bool
	Frames  []frameView
}

// frameView is one replayed rrweb event.
type frameView struct {
	Index int
	Frame json.RawMessage
}

// listJSON is the list page's data island.
type listJSON struct {
	Runs []RunSummary `json:"runs"`
}

// runJSON is the detail page's data island.
type runJSON struct {
	Run    runPublic `json:"run"`
	Events []Event   `json:"events"`
}

// liveJSON is the answer of the hydration endpoint.
type liveJSON struct {
	Run    runPublic `json:"run"`
	Events []Event   `json:"events"`
}

// recordingJSON is the answer of the recording endpoint.
type recordingJSON struct {
	Frames []json.RawMessage `json:"events"`
}

// errorBody is the stable error code of a failed JSON request.
type errorBody struct {
	Code core.Code `json:"code"`
}

// errorJSON is an error response.
type errorJSON struct {
	Error errorBody `json:"error"`
}

// runPublic is the rendered part of a run: the scope fields never leave the
// process, and a recording is summarised instead of sending every frame twice.
type runPublic struct {
	ID        string            `json:"id"`
	Flow      string            `json:"flow"`
	State     string            `json:"state"`
	ErrorCode string            `json:"error_code,omitempty"`
	Gates     []Gate            `json:"gates"`
	Recording recordingJSONView `json:"recording"`
}

// recordingJSONView summarises the recording for the page.
type recordingJSONView struct {
	State   string `json:"state"`
	Events  int    `json:"events"`
	Stopped bool   `json:"stopped"`
}

func publicRun(run Run) runPublic {
	recording := recordingJSONView{State: run.Recording.State()}
	if run.Recording != nil && !run.Recording.Err {
		recording.Events = len(run.Recording.Frames)
		recording.Stopped = true
	}
	gates := run.Gates
	if gates == nil {
		gates = []Gate{}
	}
	return runPublic{
		ID:        run.ID,
		Flow:      run.Flow,
		State:     run.State,
		ErrorCode: run.ErrorCode,
		Gates:     gates,
		Recording: recording,
	}
}

// recordingViewOf builds the recording section. A replay always runs to the
// last frame; there is no live tail to wait for (REQ-INSP-010).
func recordingViewOf(rec *Recording) recordingView {
	view := recordingView{State: rec.State(), Count: len(recFrames(rec))}
	if view.State != "present" {
		return view
	}
	view.Stopped = true
	view.Frames = make([]frameView, 0, view.Count)
	for i, frame := range recFrames(rec) {
		view.Frames = append(view.Frames, frameView{Index: i + 1, Frame: frame})
	}
	return view
}

func recFrames(rec *Recording) []json.RawMessage {
	if rec == nil || rec.Err {
		return nil
	}
	return rec.Frames
}
