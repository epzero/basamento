package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/epzero/basamento/api/inspector"
	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/expr"
)

// inspectorWorld drives the run inspector at its HTTP boundary: a real
// inspector.Handler over an in-memory tenant-scoped Source, reached with
// net/http as the browser would. It stands in for "kit serve" with the
// inspector mounted until the browser runner (WP-L9-01) replays the @journey
// lane; nothing here uses a clock, a sleep or a vendor.
type inspectorWorld struct {
	mu       sync.Mutex
	tenant   core.TenantID
	flows    map[string]bool
	accounts map[string]*inspectorUser
	runs     map[string]*inspectorFixtureRun
	order    []string

	server *httptest.Server
	client *http.Client

	principal     string
	openRun       string
	liveRun       string
	view          *inspectorView
	listed        []inspectorRunSummary
	lastStatus    int
	lastRejection string
	lastBody      string
	continued     map[string]json.RawMessage
}

type inspectorUser struct {
	tenant   core.TenantID
	projects map[string]bool
	canRead  bool
}

type inspectorFixtureRun struct {
	id        string
	flow      string
	tenant    core.TenantID
	project   string
	state     string
	errorCode string
	events    []inspector.Event
	gates     []inspector.Gate
	recording *inspector.Recording
}

type inspectorView struct {
	runID            string
	state            string
	errorCode        string
	gates            []inspector.Gate
	events           []int64
	recordingState   string
	recordingEvents  int
	recordingStopped bool
	denied           bool
}

type inspectorRunSummary struct {
	ID    string `json:"id"`
	Flow  string `json:"flow"`
	State string `json:"state"`
}

var (
	inspectorRunIsland  = regexp.MustCompile(`(?s)<script id="inspector-run" type="application/json">(.*?)</script>`)
	inspectorRunsIsland = regexp.MustCompile(`(?s)<script id="inspector-runs" type="application/json">(.*?)</script>`)
	inspectorRejection  = regexp.MustCompile(`data-rejection-code="([^"]+)"`)
)

// islandRun is the detail page's data island.
type islandRun struct {
	Run struct {
		ID        string `json:"id"`
		Flow      string `json:"flow"`
		State     string `json:"state"`
		ErrorCode string `json:"error_code"`
		Gates     []struct {
			Node    string `json:"node"`
			Accepts string `json:"accepts"`
		} `json:"gates"`
		Recording struct {
			State   string `json:"state"`
			Events  int    `json:"events"`
			Stopped bool   `json:"stopped"`
		} `json:"recording"`
	} `json:"run"`
	Events []struct {
		Seq  int64  `json:"seq"`
		Type string `json:"type"`
	} `json:"events"`
}

// islandRuns is the list page's data island.
type islandRuns struct {
	Runs []inspectorRunSummary `json:"runs"`
}

func newInspectorWorld() *inspectorWorld {
	return &inspectorWorld{
		flows:     map[string]bool{},
		accounts:  map[string]*inspectorUser{},
		runs:      map[string]*inspectorFixtureRun{},
		continued: map[string]json.RawMessage{},
	}
}

func (w *inspectorWorld) teardown() {
	if w.server != nil {
		w.server.Close()
		w.server = nil
	}
}

// start builds the handler once. The authenticator maps the driver's
// X-Principal header to an account, as an OIDC adapter maps a bearer token to
// a principal in kit serve.
func (w *inspectorWorld) start() error {
	if w.server != nil {
		return nil
	}
	handler, err := inspector.New(inspector.Options{
		Source: w,
		Authenticator: inspector.AuthenticatorFunc(func(_ context.Context, r *http.Request) (authn.Principal, error) {
			name := r.Header.Get("X-Principal")
			acc, ok := w.accountOf(name)
			if !ok {
				return authn.Principal{}, core.NewError(core.CodeUnauthenticated, "unknown principal", nil)
			}
			return authn.Principal{ID: name, Tenant: acc.tenant}, nil
		}),
	})
	if err != nil {
		return err
	}
	w.server = httptest.NewServer(handler)
	w.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return nil
}

func (w *inspectorWorld) accountOf(name string) (*inspectorUser, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	acc, ok := w.accounts[name]
	return acc, ok
}

// Source: List returns the runs of the principal's projects.
func (w *inspectorWorld) List(_ context.Context, p authn.Principal) ([]inspector.RunSummary, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	acc, ok := w.accounts[p.ID]
	if !ok {
		return nil, core.NewError(core.CodePermissionDenied, "no account", nil)
	}
	if !acc.canRead {
		return nil, core.NewError(core.CodePermissionDenied, "the account may not read runs", nil)
	}
	out := make([]inspector.RunSummary, 0, len(w.order))
	for _, id := range w.order {
		run := w.runs[id]
		if run.tenant != p.Tenant || !acc.projects[run.project] {
			continue
		}
		out = append(out, inspector.RunSummary{ID: run.id, Flow: run.flow, State: run.state, Tenant: run.tenant})
	}
	return out, nil
}

// Source: Run returns one run the principal may read. Another tenant's run and
// another project's run are both not_found, never permission_denied: the
// inspector cannot tell them from a missing run (REQ-INSP-002).
func (w *inspectorWorld) Run(_ context.Context, p authn.Principal, id string) (inspector.Run, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	acc, ok := w.accounts[p.ID]
	if !ok || !acc.canRead {
		return inspector.Run{}, core.NewError(core.CodePermissionDenied, "the account may not read runs", nil)
	}
	run, ok := w.runs[id]
	if !ok || run.tenant != p.Tenant || !acc.projects[run.project] {
		return inspector.Run{}, core.NewError(core.CodeNotFound, "run not found", nil)
	}
	return inspector.Run{
		ID:        run.id,
		Flow:      run.flow,
		State:     run.state,
		ErrorCode: run.errorCode,
		Gates:     append([]inspector.Gate(nil), run.gates...),
		Recording: run.recording,
		Tenant:    run.tenant,
		Project:   run.project,
	}, nil
}

// Source: Events returns durable events after seq, in sequence order.
func (w *inspectorWorld) Events(ctx context.Context, p authn.Principal, id string, after int64) ([]inspector.Event, error) {
	if _, err := w.Run(ctx, p, id); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	run := w.runs[id]
	out := make([]inspector.Event, 0, len(run.events))
	for _, e := range run.events {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// Source: Resolve validates a payload with the gate's accept expression, as
// the flow engine does, and keeps the gate open when it refuses.
func (w *inspectorWorld) Resolve(ctx context.Context, p authn.Principal, id, gate string, payload json.RawMessage) error {
	if _, err := w.Run(ctx, p, id); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	run := w.runs[id]
	index := -1
	for i, g := range run.gates {
		if g.Node == gate {
			index = i
			break
		}
	}
	if index < 0 {
		return core.NewError(core.CodeNotFound, "gate not open", nil)
	}
	ok, err := acceptsPayload(run.gates[index].Accepts, payload)
	if err != nil {
		return err
	}
	if !ok {
		return core.NewError(core.CodeInvalidArgument, "the gate does not accept the payload", nil)
	}
	run.gates = append(run.gates[:index], run.gates[index+1:]...)
	run.events = append(run.events, inspector.Event{
		Seq:  int64(len(run.events) + 1),
		Type: "gate.resolved",
		Data: payload,
	})
	w.continued[id+"/"+gate] = payload
	return nil
}

// acceptsPayload evaluates a gate's accept expression over the payload.
func acceptsPayload(accepts string, payload json.RawMessage) (bool, error) {
	var input any
	if err := json.Unmarshal(payload, &input); err != nil {
		return false, core.NewError(core.CodeInvalidArgument, "the payload is not JSON", nil)
	}
	env, err := expr.NewEnv([]expr.Var{{Name: "input", Schema: json.RawMessage(`true`)}})
	if err != nil {
		return false, err
	}
	program, err := env.Compile(accepts, json.RawMessage(`true`))
	if err != nil {
		return false, err
	}
	value, err := program.Eval(map[string]any{"input": input})
	if err != nil {
		return false, core.NewError(core.CodeInvalidArgument, "the payload cannot be evaluated", nil)
	}
	accepted, ok := value.(bool)
	if !ok {
		return false, core.NewError(core.CodeInvalidArgument, "the gate expression is not a boolean", nil)
	}
	return accepted, nil
}

// --- Scenario setup -------------------------------------------------------

func (w *inspectorWorld) seedAccount(name, tenant, project string, canRead bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	acc, ok := w.accounts[name]
	if !ok {
		acc = &inspectorUser{projects: map[string]bool{}}
		w.accounts[name] = acc
	}
	acc.tenant = core.TenantID(tenant)
	acc.projects[project] = true
	acc.canRead = canRead
}

func (w *inspectorWorld) seedRun(id, flow, tenant, project, state, errorCode string, events int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run, ok := w.runs[id]
	if !ok {
		run = &inspectorFixtureRun{id: id, project: "p1"}
		w.runs[id] = run
		w.order = append(w.order, id)
	}
	run.flow = flow
	if tenant != "" {
		run.tenant = core.TenantID(tenant)
	} else if run.tenant == "" {
		run.tenant = w.tenant
	}
	if project != "" {
		run.project = project
	}
	run.state = state
	run.errorCode = errorCode
	for len(run.events) < events {
		seq := int64(len(run.events) + 1)
		run.events = append(run.events, inspector.Event{
			Seq:  seq,
			Type: "node.completed",
			Data: json.RawMessage(fmt.Sprintf(`{"n":%d}`, seq)),
		})
	}
}

func (w *inspectorWorld) seedGate(id, flow, gate, accepts string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run, ok := w.runs[id]
	if !ok {
		run = &inspectorFixtureRun{id: id, project: "p1", state: "waiting", tenant: w.tenant}
		w.runs[id] = run
		w.order = append(w.order, id)
	}
	if run.tenant == "" {
		run.tenant = w.tenant
	}
	run.flow = flow
	run.state = "waiting"
	w.flows[flow] = true
	for i, g := range run.gates {
		if g.Node == gate {
			run.gates[i].Accepts = accepts
			return
		}
	}
	run.gates = append(run.gates, inspector.Gate{Node: gate, Accepts: accepts})
}

func (w *inspectorWorld) seedRecording(id, flow string, events int, broken bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run, ok := w.runs[id]
	if !ok {
		run = &inspectorFixtureRun{id: id, project: "p1", state: "completed", tenant: w.tenant}
		w.runs[id] = run
		w.order = append(w.order, id)
	}
	if run.tenant == "" {
		run.tenant = w.tenant
	}
	run.flow = flow
	w.flows[flow] = true
	if broken {
		run.recording = &inspector.Recording{Err: true}
		return
	}
	frames := make([]json.RawMessage, events)
	for i := range frames {
		frames[i] = json.RawMessage(fmt.Sprintf(`{"index":%d}`, i+1))
	}
	run.recording = &inspector.Recording{Frames: frames}
}

func (w *inspectorWorld) addEvents(n int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := w.liveRun
	if id == "" {
		id = w.openRun
	}
	run, ok := w.runs[id]
	if !ok {
		return fmt.Errorf("no run to add events to")
	}
	for i := 0; i < n; i++ {
		seq := int64(len(run.events) + 1)
		run.events = append(run.events, inspector.Event{
			Seq:  seq,
			Type: "node.completed",
			Data: json.RawMessage(fmt.Sprintf(`{"n":%d}`, seq)),
		})
	}
	return nil
}

func (w *inspectorWorld) reachGate(id, gate, accepts string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run, ok := w.runs[id]
	if !ok {
		return
	}
	for _, g := range run.gates {
		if g.Node == gate {
			return
		}
	}
	run.gates = append(run.gates, inspector.Gate{Node: gate, Accepts: accepts})
}

func (w *inspectorWorld) fail(id, code string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if run, ok := w.runs[id]; ok {
		run.state = "failed"
		run.errorCode = code
	}
}

func (w *inspectorWorld) gateStillOpen(id, gate string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	run, ok := w.runs[id]
	if !ok {
		return false
	}
	for _, g := range run.gates {
		if g.Node == gate {
			return true
		}
	}
	return false
}

func (w *inspectorWorld) continuedPayload(id, gate string) (json.RawMessage, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	payload, ok := w.continued[id+"/"+gate]
	return payload, ok
}

func (w *inspectorWorld) runTenant(id string) (core.TenantID, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run, ok := w.runs[id]
	if !ok {
		return "", false
	}
	return run.tenant, true
}

// --- HTTP driver ----------------------------------------------------------

func (w *inspectorWorld) get(principal, path string) (int, string, error) {
	if err := w.start(); err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, w.server.URL+path, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("X-Principal", principal)
	resp, err := w.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

func (w *inspectorWorld) post(principal, runID, gate, payload string) (int, string, error) {
	if err := w.start(); err != nil {
		return 0, "", err
	}
	form := url.Values{"payload": {payload}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		w.server.URL+"/runs/"+url.PathEscape(runID)+"/gates/"+url.PathEscape(gate), strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("X-Principal", principal)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := w.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

func (w *inspectorWorld) open(principal, id string) error {
	status, body, err := w.get(principal, "/runs/"+url.PathEscape(id))
	if err != nil {
		return err
	}
	w.principal = principal
	w.lastStatus = status
	w.lastBody = body
	w.liveRun = id
	if status != http.StatusOK {
		w.openRun = ""
		w.view = &inspectorView{runID: id, denied: true}
		return nil
	}
	island, err := parseRunIsland(body)
	if err != nil {
		return err
	}
	w.openRun = id
	w.view = viewOf(island)
	return nil
}

func (w *inspectorWorld) leave() {
	w.openRun = ""
	w.view = nil
}

func (w *inspectorWorld) openList(principal string) error {
	status, body, err := w.get(principal, "/")
	if err != nil {
		return err
	}
	w.principal = principal
	w.lastStatus = status
	w.lastBody = body
	if status != http.StatusOK {
		return fmt.Errorf("the inspector answered %d", status)
	}
	match := inspectorRunsIsland.FindStringSubmatch(body)
	if match == nil {
		return fmt.Errorf("the list page has no data island")
	}
	var island islandRuns
	if err := json.Unmarshal([]byte(match[1]), &island); err != nil {
		return err
	}
	w.listed = island.Runs
	return nil
}

// refresh asks the hydration endpoint for what changed since the page opened,
// which is what the embedded script does while a run is shown.
func (w *inspectorWorld) refresh() error {
	if w.view == nil || w.view.denied || w.openRun == "" {
		return nil
	}
	after := int64(0)
	if len(w.view.events) > 0 {
		after = w.view.events[len(w.view.events)-1]
	}
	status, body, err := w.get(w.principal, fmt.Sprintf("/runs/%s/live?after=%d", url.PathEscape(w.openRun), after))
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("the live endpoint answered %d", status)
	}
	var island islandRun
	if err := json.Unmarshal([]byte(body), &island); err != nil {
		return err
	}
	w.view.state = island.Run.State
	w.view.errorCode = island.Run.ErrorCode
	w.view.gates = gatesOf(island)
	w.view.recordingState = island.Run.Recording.State
	w.view.recordingEvents = island.Run.Recording.Events
	w.view.recordingStopped = island.Run.Recording.Stopped
	for _, event := range island.Events {
		w.view.events = append(w.view.events, event.Seq)
	}
	return nil
}

func (w *inspectorWorld) resolve(principal, gate, runID, payload string) error {
	status, body, err := w.post(principal, runID, gate, payload)
	if err != nil {
		return err
	}
	w.principal = principal
	w.lastStatus = status
	w.lastBody = body
	switch status {
	case http.StatusSeeOther:
		w.lastRejection = ""
		return w.open(principal, runID)
	case http.StatusUnprocessableEntity:
		if match := inspectorRejection.FindStringSubmatch(body); match != nil {
			w.lastRejection = match[1]
		}
		island, err := parseRunIsland(body)
		if err != nil {
			return err
		}
		w.openRun = runID
		w.liveRun = runID
		w.view = viewOf(island)
		return nil
	default:
		return fmt.Errorf("resolving the gate answered %d: %s", status, body)
	}
}

func (w *inspectorWorld) recordingFrames(principal, runID string) ([]json.RawMessage, error) {
	status, body, err := w.get(principal, "/runs/"+url.PathEscape(runID)+"/recording")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("the recording endpoint answered %d", status)
	}
	var got struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		return nil, err
	}
	return got.Events, nil
}

func parseRunIsland(body string) (islandRun, error) {
	match := inspectorRunIsland.FindStringSubmatch(body)
	if match == nil {
		return islandRun{}, fmt.Errorf("the run page has no data island")
	}
	var island islandRun
	if err := json.Unmarshal([]byte(match[1]), &island); err != nil {
		return islandRun{}, err
	}
	return island, nil
}

func viewOf(island islandRun) *inspectorView {
	view := &inspectorView{
		runID:            island.Run.ID,
		state:            island.Run.State,
		errorCode:        island.Run.ErrorCode,
		gates:            gatesOf(island),
		recordingState:   island.Run.Recording.State,
		recordingEvents:  island.Run.Recording.Events,
		recordingStopped: island.Run.Recording.Stopped,
	}
	for _, event := range island.Events {
		view.events = append(view.events, event.Seq)
	}
	return view
}

func gatesOf(island islandRun) []inspector.Gate {
	out := make([]inspector.Gate, 0, len(island.Run.Gates))
	for _, gate := range island.Run.Gates {
		out = append(out, inspector.Gate{Node: gate.Node, Accepts: gate.Accepts})
	}
	return out
}

func seqList(events []int64) string {
	parts := make([]string, len(events))
	for i, seq := range events {
		parts[i] = strconv.FormatInt(seq, 10)
	}
	return strings.Join(parts, ",")
}
