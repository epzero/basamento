package inspector

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
)

// fakeSource is an in-memory Source. It deliberately returns whatever it is
// given, including another tenant's run, to prove the handler filters again.
type fakeSource struct {
	runs    map[string]Run
	events  map[string][]Event
	list    []RunSummary
	resolve error
	calls   []string
}

func (f *fakeSource) List(context.Context, authn.Principal) ([]RunSummary, error) {
	return f.list, nil
}

func (f *fakeSource) Run(_ context.Context, _ authn.Principal, id string) (Run, error) {
	run, ok := f.runs[id]
	if !ok {
		return Run{}, core.NewError(core.CodeNotFound, "no run", nil)
	}
	return run, nil
}

func (f *fakeSource) Events(_ context.Context, _ authn.Principal, id string, after int64) ([]Event, error) {
	var out []Event
	for _, e := range f.events[id] {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeSource) Resolve(_ context.Context, _ authn.Principal, id, gate string, payload json.RawMessage) error {
	f.calls = append(f.calls, id+"/"+gate+"/"+string(payload))
	if f.resolve != nil {
		return f.resolve
	}
	run := f.runs[id]
	gates := run.Gates[:0]
	for _, g := range run.Gates {
		if g.Node != gate {
			gates = append(gates, g)
		}
	}
	run.Gates = gates
	f.runs[id] = run
	return nil
}

func testHandler(t *testing.T, src Source, prefix string) *Handler {
	t.Helper()
	h, err := New(Options{
		Source: src,
		Prefix: prefix,
		Authenticator: AuthenticatorFunc(func(_ context.Context, r *http.Request) (authn.Principal, error) {
			name := r.Header.Get("X-Principal")
			if name == "" {
				return authn.Principal{}, core.NewError(core.CodeUnauthenticated, "no principal", nil)
			}
			return authn.Principal{ID: name, Tenant: core.TenantID("acme")}, nil
		}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func do(h http.Handler, method, target, principal string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if principal != "" {
		req.Header.Set("X-Principal", principal)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNewRequiresSourceAndAuthenticator(t *testing.T) {
	if _, err := New(Options{}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("New without source: %v, want invalid_argument", err)
	}
	if _, err := New(Options{Source: &fakeSource{}}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("New without authenticator: %v, want invalid_argument", err)
	}
}

func TestListDropsAnotherTenantsRunEvenIfSourceReturnsIt(t *testing.T) {
	src := &fakeSource{list: []RunSummary{
		{ID: "r1", Flow: "review", State: "running", Tenant: "acme"},
		{ID: "g1", Flow: "review", State: "running", Tenant: "globex"},
	}}
	h := testHandler(t, src, "")
	rec := do(h, http.MethodGet, "/", "ana", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-run-id="r1"`) {
		t.Fatalf("page does not list r1:\n%s", body)
	}
	if strings.Contains(body, "g1") || strings.Contains(body, "globex") {
		t.Fatalf("page leaks another tenant's run:\n%s", body)
	}
}

func TestDetailOfAnotherTenantIsNotFoundWithoutData(t *testing.T) {
	src := &fakeSource{
		runs:   map[string]Run{"g1": {ID: "g1", Flow: "review", State: "running", Tenant: "globex"}},
		events: map[string][]Event{"g1": {{Seq: 1, Type: "run.started"}}},
	}
	h := testHandler(t, src, "")
	rec := do(h, http.MethodGet, "/runs/g1", "ana", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	for _, leaked := range []string{"review", "run.started", `data-run-id`} {
		if strings.Contains(body, leaked) {
			t.Fatalf("denial leaks %q:\n%s", leaked, body)
		}
	}
	if !strings.Contains(body, `data-denied="true"`) {
		t.Fatalf("denial page missing marker:\n%s", body)
	}
}

func TestDetailShowsOrderedEventsAndFailedState(t *testing.T) {
	src := &fakeSource{
		runs: map[string]Run{"r3": {ID: "r3", Flow: "assistant", State: "failed", ErrorCode: "vendor_error", Tenant: "acme"}},
		events: map[string][]Event{"r3": {
			{Seq: 3, Type: "run.failed"},
			{Seq: 1, Type: "run.started"},
			{Seq: 2, Type: "node.started"},
		}},
	}
	h := testHandler(t, src, "")
	rec := do(h, http.MethodGet, "/runs/r3", "ana", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-state="failed"`) || !strings.Contains(body, `data-error-code="vendor_error"`) {
		t.Fatalf("page does not show failed and its code:\n%s", body)
	}
	first := strings.Index(body, `data-seq="1"`)
	second := strings.Index(body, `data-seq="2"`)
	third := strings.Index(body, `data-seq="3"`)
	if first < 0 || first >= second || second >= third {
		t.Fatalf("events are not in sequence order:\n%s", body)
	}
}

func TestLiveReturnsOnlyEventsAfterTheSequence(t *testing.T) {
	src := &fakeSource{
		runs: map[string]Run{"r1": {ID: "r1", Flow: "review", State: "running", Tenant: "acme"}},
		events: map[string][]Event{"r1": {
			{Seq: 1, Type: "run.started"},
			{Seq: 2, Type: "node.started"},
			{Seq: 3, Type: "node.completed"},
		}},
	}
	h := testHandler(t, src, "")
	rec := do(h, http.MethodGet, "/runs/r1/live?after=1", "ana", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got liveJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Events) != 2 || got.Events[0].Seq != 2 || got.Events[1].Seq != 3 {
		t.Fatalf("events = %+v, want seq 2 and 3", got.Events)
	}
}

func TestGatesAreListedWithTheirPayloadExpression(t *testing.T) {
	src := &fakeSource{
		runs: map[string]Run{"r1": {ID: "r1", Flow: "review", State: "waiting", Tenant: "acme", Gates: []Gate{
			{Node: "approve", Accepts: "input.approved || size(input.notes) > 0"},
		}}},
	}
	h := testHandler(t, src, "")
	body := do(h, http.MethodGet, "/runs/r1", "ana", nil).Body.String()
	if !strings.Contains(body, `data-gate="approve"`) {
		t.Fatalf("gate not listed:\n%s", body)
	}
	if !strings.Contains(body, html.EscapeString("input.approved || size(input.notes) > 0")) {
		t.Fatalf("gate payload expression not shown:\n%s", body)
	}
}

func TestRejectedPayloadIsExplainedAndTheGateStaysOpen(t *testing.T) {
	src := &fakeSource{
		runs: map[string]Run{"r1": {ID: "r1", Flow: "review", State: "waiting", Tenant: "acme", Gates: []Gate{
			{Node: "approve", Accepts: "input.approved"},
		}}},
		resolve: core.NewError(core.CodeInvalidArgument, "the gate does not accept this payload", nil),
	}
	h := testHandler(t, src, "")
	rec := do(h, http.MethodPost, "/runs/r1/gates/approve", "ana", url.Values{"payload": {`{"approved":false}`}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-rejection-code="invalid_argument"`) {
		t.Fatalf("rejection is not explained:\n%s", body)
	}
	if !strings.Contains(body, `data-gate="approve"`) {
		t.Fatalf("the gate is no longer listed:\n%s", body)
	}
}

func TestAcceptedPayloadRedirectsToTheRun(t *testing.T) {
	src := &fakeSource{
		runs: map[string]Run{"r1": {ID: "r1", Flow: "review", State: "waiting", Tenant: "acme", Gates: []Gate{
			{Node: "approve", Accepts: "input.approved"},
		}}},
	}
	h := testHandler(t, src, "/inspector")
	mounted := http.StripPrefix("/inspector", h)
	rec := do(mounted, http.MethodPost, "/inspector/runs/r1/gates/approve", "ana", url.Values{"payload": {`{"approved":true}`}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/inspector/runs/r1" {
		t.Fatalf("Location = %q, want /inspector/runs/r1", got)
	}
	if len(src.calls) != 1 || src.calls[0] != `r1/approve/{"approved":true}` {
		t.Fatalf("resolve calls = %v", src.calls)
	}
}

func TestRecordingStates(t *testing.T) {
	frames := make([]json.RawMessage, 3)
	for i := range frames {
		frames[i] = json.RawMessage(`{"type":4}`)
	}
	src := &fakeSource{
		runs: map[string]Run{
			"present": {ID: "present", Tenant: "acme", Recording: &Recording{Frames: frames}},
			"none":    {ID: "none", Tenant: "acme"},
			"broken":  {ID: "broken", Tenant: "acme", Recording: &Recording{Err: true}},
		},
	}
	h := testHandler(t, src, "")

	body := do(h, http.MethodGet, "/runs/present", "ana", nil).Body.String()
	if !strings.Contains(body, `data-recording="present"`) || !strings.Contains(body, `data-recording-events="3"`) {
		t.Fatalf("present recording not rendered:\n%s", body)
	}
	if !strings.Contains(body, `data-replay-state="stopped"`) {
		t.Fatalf("replay is not shown as stopped:\n%s", body)
	}

	body = do(h, http.MethodGet, "/runs/none", "ana", nil).Body.String()
	if !strings.Contains(body, `data-recording="none"`) || !strings.Contains(body, "no recording to replay") {
		t.Fatalf("missing recording not explained:\n%s", body)
	}

	body = do(h, http.MethodGet, "/runs/broken", "ana", nil).Body.String()
	if !strings.Contains(body, `data-recording="error"`) || !strings.Contains(body, "cannot be replayed") {
		t.Fatalf("broken recording not explained:\n%s", body)
	}
}

func TestRecordingEndpointReturnsFramesInOrder(t *testing.T) {
	src := &fakeSource{
		runs: map[string]Run{"r1": {ID: "r1", Tenant: "acme", Recording: &Recording{Frames: []json.RawMessage{
			json.RawMessage(`{"index":1}`),
			json.RawMessage(`{"index":2}`),
		}}}},
	}
	h := testHandler(t, src, "")
	rec := do(h, http.MethodGet, "/runs/r1/recording", "ana", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got recordingJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Frames) != 2 || !strings.Contains(string(got.Frames[0]), `"index":1`) {
		t.Fatalf("frames = %s", got.Frames)
	}
}

func TestUnauthenticatedIsDenied(t *testing.T) {
	h := testHandler(t, &fakeSource{}, "")
	if rec := do(h, http.MethodGet, "/", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAssetsAreServedWithoutAPrincipal(t *testing.T) {
	h := testHandler(t, &fakeSource{}, "")
	rec := do(h, http.MethodGet, "/assets/app.css", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "--accent") {
		t.Fatalf("css not served:\n%s", rec.Body.String())
	}
}

func TestPrefixIsUsedInLinksAndAssets(t *testing.T) {
	src := &fakeSource{list: []RunSummary{{ID: "r1", Flow: "review", State: "running", Tenant: "acme"}}}
	h := testHandler(t, src, "/inspector/")
	mounted := http.StripPrefix("/inspector", h)
	body := do(mounted, http.MethodGet, "/inspector/", "ana", nil).Body.String()
	if !strings.Contains(body, `href="/inspector/runs/r1"`) {
		t.Fatalf("link is not under the mount prefix:\n%s", body)
	}
	if !strings.Contains(body, `src="/inspector/assets/app.js"`) {
		t.Fatalf("asset is not under the mount prefix:\n%s", body)
	}
}
