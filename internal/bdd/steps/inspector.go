package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/api/inspector"
	"github.com/epzero/basamento/core"
)

// registerInspector registers the run inspector's journey phrases. The
// scenarios are @journey, so they are excluded from the deterministic gate
// run and replayed by the browser runner; this driver executes the same
// phrases against the server-rendered inspector so the behavior is provable
// before WP-L9-01 lands.
func registerInspector(sc *godog.ScenarioContext) {
	sc.Step(`^"kit serve" runs with the inspector, tenant "([^"]+)", and flow "([^"]+)"$`, inspectorServeOne)
	sc.Step(`^"kit serve" runs with the inspector, tenant "([^"]+)", and flows "([^"]+)" and "([^"]+)"$`, inspectorServeTwo)
	sc.Step(`^account "([^"]+)" is in project "([^"]+)" of tenant "([^"]+)"$`, inspectorAccount)
	sc.Step(`^account "([^"]+)" is in project "([^"]+)" of tenant "([^"]+)" without the permission to read runs$`, inspectorAccountNoRead)
	sc.Step(`^flow "([^"]+)" has gates "([^"]+)" and "([^"]+)"$`, inspectorFlowGates)

	sc.Step(`^run "([^"]+)" of "([^"]+)" waits at gate "([^"]+)", which accepts payloads where "([^"]+)"$`, inspectorWaitAtGate)
	sc.Step(`^run "([^"]+)" of "([^"]+)" is running$`, inspectorRunRunning)
	sc.Step(`^run "([^"]+)" of "([^"]+)" is running in project "([^"]+)" with (\d+) events$`, inspectorRunRunningWithEvents)
	sc.Step(`^run "([^"]+)" of "([^"]+)" is completed in project "([^"]+)" with (\d+) events$`, inspectorRunCompletedWithEvents)
	sc.Step(`^run "([^"]+)" of "([^"]+)" in project "([^"]+)" has state "([^"]+)" with error code "([^"]+)"$`, inspectorRunFailed)
	sc.Step(`^run "([^"]+)" of "([^"]+)" in project "([^"]+)" has state "([^"]+)"$`, inspectorRunInState)
	sc.Step(`^tenant "([^"]+)" has run "([^"]+)" of their flow "([^"]+)"$`, inspectorOtherTenantRun)
	sc.Step(`^run "([^"]+)" of "([^"]+)" is in project "([^"]+)" of tenant "([^"]+)"$`, inspectorRunInProject)
	sc.Step(`^run "([^"]+)" of "([^"]+)" has an rrweb recording of (\d+) events$`, inspectorRecording)
	sc.Step(`^run "([^"]+)" of "([^"]+)" has no rrweb recording$`, inspectorNoRecording)
	sc.Step(`^run "([^"]+)" of "([^"]+)" has an rrweb recording that cannot be loaded$`, inspectorBrokenRecording)

	sc.Step(`^([A-Za-z]+) has opened run "([^"]+)"$`, inspectorHasOpened)
	sc.Step(`^([A-Za-z]+) left the inspector after opening run "([^"]+)"$`, inspectorLeft)
	sc.Step(`^([A-Za-z]+) resolved gate "([^"]+)" of run "([^"]+)" with payload (\{.*\}) and it was rejected$`, inspectorResolvedRejected)

	sc.Step(`^([A-Za-z]+) opens the inspector$`, inspectorOpensList)
	sc.Step(`^([A-Za-z]+) opens run "([^"]+)"(?: again)?$`, inspectorOpensRun)
	sc.Step(`^([A-Za-z]+) opens run "([^"]+)" and replays its recording$`, inspectorOpensAndReplays)
	sc.Step(`^([A-Za-z]+) opens a link to run "([^"]+)" of tenant "([^"]+)"$`, inspectorOpensLink)
	sc.Step(`^([A-Za-z]+) resolves gate "([^"]+)" of run "([^"]+)" with payload (\{.*\})$`, inspectorResolves)
	sc.Step(`^run "([^"]+)" reaches gate "([^"]+)", which accepts payloads where "([^"]+)"$`, inspectorReachesGate)
	sc.Step(`^run "([^"]+)" fails with error code "([^"]+)"$`, inspectorFails)
	sc.Step(`^the run adds (\d+) more events?$`, inspectorAddsEvents)

	sc.Step(`^([A-Za-z]+) sees the runs "([^"]+)", "([^"]+)" and "([^"]+)", each with its id, flow and state$`, inspectorSeesRunsDetailed)
	sc.Step(`^([A-Za-z]+) sees the runs "([^"]+)", "([^"]+)" and "([^"]+)"$`, inspectorSeesRuns)
	sc.Step(`^([A-Za-z]+) does not see run "([^"]+)" or any run of tenant "([^"]+)"$`, inspectorDoesNotSeeTenant)
	sc.Step(`^([A-Za-z]+) does not see run "([^"]+)"$`, inspectorDoesNotSee)
	sc.Step(`^([A-Za-z]+) is denied access to run "([^"]+)"$`, inspectorDenied)
	sc.Step(`^([A-Za-z]+) sees no event, gate or state of run "([^"]+)"$`, inspectorSeesNothing)
	sc.Step(`^([A-Za-z]+) sees no flow, state, event or gate of run "([^"]+)"$`, inspectorSeesNothing)

	sc.Step(`^([A-Za-z]+) sees the (\d+) events of run "([^"]+)" in sequence order$`, inspectorSeesEvents)
	sc.Step(`^the state ([A-Za-z]+) sees for run "([^"]+)" is "([^"]+)"$`, inspectorStateSeen)
	sc.Step(`^([A-Za-z]+) sees the state "([^"]+)" for run "([^"]+)"$`, inspectorSeesState)
	sc.Step(`^([A-Za-z]+) sees the error code "([^"]+)" for run "([^"]+)"$`, inspectorSeesErrorCode)
	sc.Step(`^([A-Za-z]+) sees no error code for run "([^"]+)"$`, inspectorSeesNoErrorCode)

	sc.Step(`^([A-Za-z]+) sees (\d+) open gates? for run "([^"]+)"$`, inspectorSeesGateCount)
	sc.Step(`^([A-Za-z]+) sees (\d+) open gates?, "([^"]+)", for run "([^"]+)"$`, inspectorSeesGate)
	sc.Step(`^([A-Za-z]+) sees (\d+) open gates?, "([^"]+)" and "([^"]+)", for run "([^"]+)"$`, inspectorSeesTwoGates)
	sc.Step(`^([A-Za-z]+) sees that gate "([^"]+)" accepts payloads where "([^"]+)"$`, inspectorSeesAccepts)
	sc.Step(`^([A-Za-z]+) sees that gate "([^"]+)" rejected the payload with code "([^"]+)"$`, inspectorSeesRejection)
	sc.Step(`^([A-Za-z]+) still sees (\d+) open gates?, "([^"]+)", for run "([^"]+)"$`, inspectorStillSeesGate)
	sc.Step(`^run "([^"]+)" still waits at gate "([^"]+)"$`, inspectorStillWaits)
	sc.Step(`^run "([^"]+)" continues past gate "([^"]+)" with payload (\{.*\})$`, inspectorContinues)

	sc.Step(`^([A-Za-z]+) sees the (\d+) events of the rrweb recording replayed in order$`, inspectorSeesReplayEvents)
	sc.Step(`^([A-Za-z]+) sees that the replay has stopped at the end of the recording$`, inspectorSeesReplayStopped)
	sc.Step(`^([A-Za-z]+) sees that run "([^"]+)" has no recording to replay$`, inspectorSeesNoRecording)
	sc.Step(`^([A-Za-z]+) sees that the recording of run "([^"]+)" cannot be replayed$`, inspectorSeesBrokenRecording)
}

func iw(ctx context.Context) (*inspectorWorld, error) {
	w := world(ctx).Inspector
	if w == nil {
		return nil, fmt.Errorf("inspector world not opened")
	}
	return w, nil
}

// --- Background -----------------------------------------------------------

func inspectorServeOne(ctx context.Context, tenant, flow string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.tenant = core.TenantID(tenant)
	w.flows[flow] = true
	return nil
}

func inspectorServeTwo(ctx context.Context, tenant, first, second string) error {
	if err := inspectorServeOne(ctx, tenant, first); err != nil {
		return err
	}
	w, _ := iw(ctx)
	w.flows[second] = true
	return nil
}

func inspectorAccount(ctx context.Context, name, project, tenant string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedAccount(name, tenant, project, true)
	return nil
}

func inspectorAccountNoRead(ctx context.Context, name, project, tenant string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedAccount(name, tenant, project, false)
	return nil
}

func inspectorFlowGates(ctx context.Context, flow, first, second string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.flows[flow] = true
	w.flows[first] = true
	w.flows[second] = true
	return nil
}

// --- Runs ----------------------------------------------------------------

func inspectorWaitAtGate(ctx context.Context, run, flow, gate, accepts string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedGate(run, flow, gate, accepts)
	return nil
}

func inspectorRunRunning(ctx context.Context, run, flow string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedRun(run, flow, "", "", "running", "", 0)
	return nil
}

func inspectorRunRunningWithEvents(ctx context.Context, run, flow, project string, events int) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedRun(run, flow, "", project, "running", "", events)
	return nil
}

func inspectorRunCompletedWithEvents(ctx context.Context, run, flow, project string, events int) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedRun(run, flow, "", project, "completed", "", events)
	return nil
}

func inspectorRunFailed(ctx context.Context, run, flow, project, state, code string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedRun(run, flow, "", project, state, code, 0)
	return nil
}

func inspectorRunInState(ctx context.Context, run, flow, project, state string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedRun(run, flow, "", project, state, "", 0)
	return nil
}

func inspectorOtherTenantRun(ctx context.Context, tenant, run, flow string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedRun(run, flow, tenant, "p1", "running", "", 0)
	return nil
}

func inspectorRunInProject(ctx context.Context, run, flow, project, tenant string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedRun(run, flow, tenant, project, "running", "", 0)
	return nil
}

func inspectorRecording(ctx context.Context, run, flow string, events int) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedRecording(run, flow, events, false)
	return nil
}

func inspectorNoRecording(ctx context.Context, run, flow string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedRun(run, flow, "", "p1", "completed", "", 0)
	return nil
}

func inspectorBrokenRecording(ctx context.Context, run, flow string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.seedRecording(run, flow, 0, true)
	return nil
}

// --- Actions --------------------------------------------------------------

func inspectorHasOpened(ctx context.Context, name, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	return w.open(name, run)
}

func inspectorLeft(ctx context.Context, name, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.open(name, run); err != nil {
		return err
	}
	w.leave()
	return nil
}

func inspectorResolvedRejected(ctx context.Context, name, gate, run, payload string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.resolve(name, gate, run, payload); err != nil {
		return err
	}
	if w.lastStatus != 422 {
		return fmt.Errorf("the rejected resolution answered %d, want 422", w.lastStatus)
	}
	return nil
}

func inspectorOpensList(ctx context.Context, name string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	return w.openList(name)
}

func inspectorOpensRun(ctx context.Context, name, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	return w.open(name, run)
}

func inspectorOpensAndReplays(ctx context.Context, name, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.open(name, run); err != nil {
		return err
	}
	_, err = w.recordingFrames(name, run)
	return err
}

func inspectorOpensLink(ctx context.Context, name, run, _ string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	return w.open(name, run)
}

func inspectorResolves(ctx context.Context, name, gate, run, payload string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	return w.resolve(name, gate, run, payload)
}

func inspectorReachesGate(ctx context.Context, run, gate, accepts string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.reachGate(run, gate, accepts)
	return nil
}

func inspectorFails(ctx context.Context, run, code string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	w.fail(run, code)
	return nil
}

func inspectorAddsEvents(ctx context.Context, n int) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	return w.addEvents(n)
}

// --- Assertions -----------------------------------------------------------

func inspectorSeesRunsDetailed(ctx context.Context, name, first, second, third string) error {
	return inspectorSeesRuns(ctx, name, first, second, third)
}

func inspectorSeesRuns(ctx context.Context, name, first, second, third string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.openList(name); err != nil {
		return err
	}
	want := map[string]bool{first: true, second: true, third: true}
	if len(w.listed) != len(want) {
		return fmt.Errorf("%s sees runs %s, want %s", name, listedIDs(w.listed), inspectorKeys(want))
	}
	for _, run := range w.listed {
		if !want[run.ID] {
			return fmt.Errorf("%s sees run %q, want only %s", name, run.ID, inspectorKeys(want))
		}
		if run.Flow == "" || run.State == "" {
			return fmt.Errorf("run %q is listed without its flow or state", run.ID)
		}
	}
	return nil
}

func inspectorDoesNotSeeTenant(ctx context.Context, name, run, tenant string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.openList(name); err != nil {
		return err
	}
	for _, listed := range w.listed {
		if listed.ID == run {
			return fmt.Errorf("%s sees run %q of tenant %q", name, run, tenant)
		}
		got, ok := w.runTenant(listed.ID)
		if ok && got == core.TenantID(tenant) {
			return fmt.Errorf("%s sees run %q of tenant %q", name, listed.ID, tenant)
		}
	}
	return nil
}

func inspectorDoesNotSee(ctx context.Context, name, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.openList(name); err != nil {
		return err
	}
	for _, listed := range w.listed {
		if listed.ID == run {
			return fmt.Errorf("%s sees run %q", name, run)
		}
	}
	return nil
}

func inspectorDenied(ctx context.Context, name, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if w.view == nil || !w.view.denied {
		return fmt.Errorf("%s was not denied run %q", name, run)
	}
	if w.lastStatus != 403 && w.lastStatus != 404 {
		return fmt.Errorf("denial answered %d, want 403 or 404", w.lastStatus)
	}
	return nil
}

func inspectorSeesNothing(ctx context.Context, name, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if w.view == nil || !w.view.denied {
		return fmt.Errorf("%s was not denied run %q", name, run)
	}
	for _, leaked := range []string{`id="inspector-run"`, `data-run-id`, `data-events`, `data-gates`, `data-state`} {
		if contains(w.lastBody, leaked) {
			return fmt.Errorf("the denial for %q leaks %q", run, leaked)
		}
	}
	return nil
}

func inspectorSeesEvents(ctx context.Context, name string, n int, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.refresh(); err != nil {
		return err
	}
	if w.view == nil || w.view.runID != run {
		return fmt.Errorf("%s is not showing run %q", name, run)
	}
	if len(w.view.events) != n {
		return fmt.Errorf("%s sees %d events of run %q, want %d", name, len(w.view.events), run, n)
	}
	for i, seq := range w.view.events {
		if seq != int64(i+1) {
			return fmt.Errorf("event %d has seq %d, want %d in sequence order (%s)", i, seq, i+1, seqList(w.view.events))
		}
	}
	return nil
}

func inspectorStateSeen(ctx context.Context, name, run, state string) error {
	return inspectorSeesState(ctx, name, state, run)
}

func inspectorSeesState(ctx context.Context, name, state, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.refresh(); err != nil {
		return err
	}
	if w.view == nil || w.view.runID != run {
		return fmt.Errorf("%s is not showing run %q", name, run)
	}
	if w.view.state != state {
		return fmt.Errorf("the state %s sees for run %q is %q, want %q", name, run, w.view.state, state)
	}
	return nil
}

func inspectorSeesErrorCode(ctx context.Context, name, code, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.refresh(); err != nil {
		return err
	}
	if w.view == nil || w.view.runID != run {
		return fmt.Errorf("%s is not showing run %q", name, run)
	}
	if w.view.errorCode != code {
		return fmt.Errorf("the error code %s sees for run %q is %q, want %q", name, run, w.view.errorCode, code)
	}
	return nil
}

func inspectorSeesNoErrorCode(ctx context.Context, name, run string) error {
	return inspectorSeesErrorCode(ctx, name, "", run)
}

func inspectorSeesGateCount(ctx context.Context, name string, n int, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.refresh(); err != nil {
		return err
	}
	if w.view == nil || w.view.runID != run {
		return fmt.Errorf("%s is not showing run %q", name, run)
	}
	if len(w.view.gates) != n {
		return fmt.Errorf("%s sees %d open gates for run %q, want %d", name, len(w.view.gates), run, n)
	}
	return nil
}

func inspectorSeesGate(ctx context.Context, name string, n int, gate, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.refresh(); err != nil {
		return err
	}
	if w.view == nil || w.view.runID != run {
		return fmt.Errorf("%s is not showing run %q", name, run)
	}
	if len(w.view.gates) != n {
		return fmt.Errorf("%s sees %d open gates for run %q, want %d", name, len(w.view.gates), run, n)
	}
	if _, ok := gateOf(w.view.gates, gate); !ok {
		return fmt.Errorf("%s does not see open gate %q for run %q", name, gate, run)
	}
	return nil
}

func inspectorSeesTwoGates(ctx context.Context, name string, n int, first, second, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.refresh(); err != nil {
		return err
	}
	if w.view == nil || w.view.runID != run {
		return fmt.Errorf("%s is not showing run %q", name, run)
	}
	if len(w.view.gates) != n {
		return fmt.Errorf("%s sees %d open gates for run %q, want %d", name, len(w.view.gates), run, n)
	}
	for _, gate := range []string{first, second} {
		if _, ok := gateOf(w.view.gates, gate); !ok {
			return fmt.Errorf("%s does not see open gate %q for run %q", name, gate, run)
		}
	}
	return nil
}

func inspectorSeesAccepts(ctx context.Context, name, gate, accepts string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if w.view == nil {
		return fmt.Errorf("%s is not showing a run", name)
	}
	got, ok := gateOf(w.view.gates, gate)
	if !ok {
		return fmt.Errorf("%s does not see gate %q", name, gate)
	}
	if got.Accepts != accepts {
		return fmt.Errorf("gate %q accepts %q, want %q", gate, got.Accepts, accepts)
	}
	return nil
}

func inspectorSeesRejection(ctx context.Context, name, gate, code string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if w.lastRejection != code {
		return fmt.Errorf("the rejection of gate %q is %q, want %q", gate, w.lastRejection, code)
	}
	return nil
}

func inspectorStillSeesGate(ctx context.Context, name string, n int, gate, run string) error {
	return inspectorSeesGate(ctx, name, n, gate, run)
}

func inspectorStillWaits(ctx context.Context, run, gate string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if !w.gateStillOpen(run, gate) {
		return fmt.Errorf("run %q does not still wait at gate %q", run, gate)
	}
	return nil
}

func inspectorContinues(ctx context.Context, run, gate, payload string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	got, ok := w.continuedPayload(run, gate)
	if !ok {
		return fmt.Errorf("run %q did not continue past gate %q", run, gate)
	}
	var wantValue, gotValue any
	if err := json.Unmarshal([]byte(payload), &wantValue); err != nil {
		return err
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		return err
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		return fmt.Errorf("run %q continued past gate %q with %s, want %s", run, gate, got, payload)
	}
	return nil
}

func inspectorSeesReplayEvents(ctx context.Context, name string, n int) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	frames, err := w.recordingFrames(name, w.liveRun)
	if err != nil {
		return err
	}
	if len(frames) != n {
		return fmt.Errorf("the recording has %d events, want %d", len(frames), n)
	}
	for i, frame := range frames {
		var got struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal(frame, &got); err != nil {
			return err
		}
		if got.Index != i+1 {
			return fmt.Errorf("recording event %d has index %d, want %d in order", i, got.Index, i+1)
		}
	}
	return nil
}

func inspectorSeesReplayStopped(ctx context.Context, name string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if err := w.refresh(); err != nil {
		return err
	}
	if w.view == nil || !w.view.recordingStopped {
		return fmt.Errorf("%s does not see the replay stopped at the end", name)
	}
	if !contains(w.lastBody, "stopped at the end of the recording") {
		return fmt.Errorf("the page does not say the replay stopped at the end")
	}
	return nil
}

func inspectorSeesNoRecording(ctx context.Context, name, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if w.view == nil || w.view.runID != run {
		return fmt.Errorf("%s is not showing run %q", name, run)
	}
	if w.view.recordingState != "none" {
		return fmt.Errorf("the recording state %s sees for run %q is %q, want none", name, run, w.view.recordingState)
	}
	if !contains(w.lastBody, "no recording to replay") {
		return fmt.Errorf("the page does not say run %q has no recording", run)
	}
	return nil
}

func inspectorSeesBrokenRecording(ctx context.Context, name, run string) error {
	w, err := iw(ctx)
	if err != nil {
		return err
	}
	if w.view == nil || w.view.runID != run {
		return fmt.Errorf("%s is not showing run %q", name, run)
	}
	if w.view.recordingState != "error" {
		return fmt.Errorf("the recording state %s sees for run %q is %q, want error", name, run, w.view.recordingState)
	}
	if !contains(w.lastBody, "cannot be replayed") {
		return fmt.Errorf("the page does not say the recording of run %q cannot be replayed", run)
	}
	return nil
}

// --- Helpers --------------------------------------------------------------

func gateOf(gates []inspector.Gate, node string) (inspector.Gate, bool) {
	for _, gate := range gates {
		if gate.Node == node {
			return gate, true
		}
	}
	return inspector.Gate{}, false
}

func listedIDs(runs []inspectorRunSummary) string {
	ids := make([]string, len(runs))
	for i, run := range runs {
		ids[i] = run.ID
	}
	return "[" + strings.Join(ids, ", ") + "]"
}

func inspectorKeys(set map[string]bool) string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return "[" + strings.Join(keys, ", ") + "]"
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
