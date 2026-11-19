package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/stream"
	"github.com/epzero/basamento/stream/eventlog"
)

const acmeToken = "acme-token"

func acmePrincipal(_ context.Context, r *http.Request) (authn.Principal, error) {
	if bearer(r) != acmeToken {
		return authn.Principal{}, core.NewError(core.CodeUnauthenticated, "no token", nil)
	}
	return authn.Principal{ID: "ana", Tenant: "acme"}, nil
}

// leakyRuns deliberately ignores the tenant in List and Describe, so a test can
// show the API filters again rather than trusting the control plane.
type leakyRuns struct {
	statuses map[core.TenantID][]runtime.RunStatus
	query    runtime.RunQuery
}

func (f *leakyRuns) List(_ context.Context, q runtime.RunQuery) ([]runtime.RunStatus, string, error) {
	f.query = q
	var out []runtime.RunStatus
	for _, runs := range f.statuses {
		out = append(out, runs...)
	}
	return out, "next", nil
}

func (f *leakyRuns) Describe(_ context.Context, ref runtime.RunRef) (runtime.RunStatus, error) {
	for _, runs := range f.statuses {
		for _, run := range runs {
			if run.Ref.RunID == ref.RunID {
				return run, nil
			}
		}
	}
	return runtime.RunStatus{}, core.NewError(core.CodeNotFound, "no run", nil)
}

func status(tenant core.TenantID, id string) runtime.RunStatus {
	return runtime.RunStatus{
		Ref:       runtime.RunRef{RunID: id, TenantID: tenant},
		State:     "running",
		StartedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
	}
}

func testServer(t *testing.T, runs Runs) *Server {
	t.Helper()
	s, err := New(Options{Runs: runs, Authenticator: AuthenticatorFunc(acmePrincipal)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func request(t *testing.T, s *Server, method, target string, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func decodeError(t *testing.T, w *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return body
}

func TestListReturnsOnlyTheRequestTenant(t *testing.T) {
	runs := &leakyRuns{statuses: map[core.TenantID][]runtime.RunStatus{
		"acme":   {status("acme", "acme-run")},
		"globex": {status("globex", "globex-run")},
	}}
	w := request(t, testServer(t, runs), http.MethodGet, "/v1/runs", acmeToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	var body runListJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Runs) != 1 || body.Runs[0].ID != "acme-run" {
		t.Fatalf("runs = %+v, want only acme-run", body.Runs)
	}
}

func TestGetAnotherTenantsRunIsNotFound(t *testing.T) {
	runs := &leakyRuns{statuses: map[core.TenantID][]runtime.RunStatus{
		"acme":   {status("acme", "acme-run")},
		"globex": {status("globex", "globex-run")},
	}}
	w := request(t, testServer(t, runs), http.MethodGet, "/v1/runs/globex-run", acmeToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body)
	}
	if got := decodeError(t, w).Error.Code; got != core.CodeNotFound {
		t.Fatalf("code = %q, want not_found", got)
	}
}

func TestReadsWithoutAPrincipalAreUnauthenticated(t *testing.T) {
	runs := &leakyRuns{}
	w := request(t, testServer(t, runs), http.MethodGet, "/v1/runs", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if got := decodeError(t, w).Error.Code; got != core.CodeUnauthenticated {
		t.Fatalf("code = %q, want unauthenticated", got)
	}
}

func TestListPassesFiltersToTheControlPlane(t *testing.T) {
	runs := &leakyRuns{}
	w := request(t, testServer(t, runs), http.MethodGet, "/v1/runs?state=completed&limit=5&cursor=abc", acmeToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	if runs.query.State != "completed" || runs.query.Limit != 5 || runs.query.Cursor != "abc" {
		t.Fatalf("query = %+v", runs.query)
	}
}

func TestListRejectsANegativeLimit(t *testing.T) {
	w := request(t, testServer(t, &leakyRuns{}), http.MethodGet, "/v1/runs?limit=-1", acmeToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

type fakeAuthorizer struct {
	err    error
	action authz.Action
}

func (f *fakeAuthorizer) Check(_ context.Context, _ authn.Principal, action authz.Action, _ authz.Resource) error {
	f.action = action
	return f.err
}

type fakeCanceller struct{ cancelled string }

func (f *fakeCanceller) Cancel(_ context.Context, ref runtime.RunRef, _ string) error {
	f.cancelled = ref.RunID
	return nil
}

func TestCancelChecksPermissionBeforeCancelling(t *testing.T) {
	canceller := &fakeCanceller{}
	authorizer := &fakeAuthorizer{err: core.NewError(core.CodePermissionDenied, "no", nil)}
	s, err := New(Options{
		Runs:          &leakyRuns{},
		Canceller:     canceller,
		Authorizer:    authorizer,
		Authenticator: AuthenticatorFunc(acmePrincipal),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := request(t, s, http.MethodPost, "/v1/runs/run-1/cancel", acmeToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body)
	}
	if canceller.cancelled != "" {
		t.Fatalf("cancelled %q despite a denial", canceller.cancelled)
	}
	if authorizer.action != authz.ActionRunCancel {
		t.Fatalf("action = %q, want run.cancel", authorizer.action)
	}
}

func TestCancelSucceedsWhenAllowed(t *testing.T) {
	canceller := &fakeCanceller{}
	s, err := New(Options{
		Runs:          &leakyRuns{},
		Canceller:     canceller,
		Authorizer:    &fakeAuthorizer{},
		Authenticator: AuthenticatorFunc(acmePrincipal),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := request(t, s, http.MethodPost, "/v1/runs/run-1/cancel", acmeToken)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", w.Code, w.Body)
	}
	if canceller.cancelled != "run-1" {
		t.Fatalf("cancelled = %q, want run-1", canceller.cancelled)
	}
}

func TestCancelWithoutAnAuthorizerIsUnsupported(t *testing.T) {
	canceller := &fakeCanceller{}
	s, err := New(Options{
		Runs:          &leakyRuns{},
		Canceller:     canceller,
		Authenticator: AuthenticatorFunc(acmePrincipal),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := request(t, s, http.MethodPost, "/v1/runs/run-1/cancel", acmeToken)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
}

func TestNewRequiresARunControlPlaneAndAuthenticator(t *testing.T) {
	if _, err := New(Options{Authenticator: AuthenticatorFunc(acmePrincipal)}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("missing runs: %v", err)
	}
	if _, err := New(Options{Runs: &leakyRuns{}}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("missing authenticator: %v", err)
	}
}

func TestStatusForEveryCode(t *testing.T) {
	cases := map[core.Code]int{
		core.CodeInvalidArgument:   http.StatusBadRequest,
		core.CodeUnauthenticated:   http.StatusUnauthorized,
		core.CodePermissionDenied:  http.StatusForbidden,
		core.CodeNotFound:          http.StatusNotFound,
		core.CodeConflict:          http.StatusConflict,
		core.CodeBudgetExceeded:    http.StatusPaymentRequired,
		core.CodeRateLimited:       http.StatusTooManyRequests,
		core.CodeTimeout:           http.StatusGatewayTimeout,
		core.CodeUnavailable:       http.StatusServiceUnavailable,
		core.CodeUnsupported:       http.StatusNotImplemented,
		core.CodeVendor:            http.StatusBadGateway,
		core.CodeCancelled:         http.StatusConflict,
		core.CodeRecordingMissing:  http.StatusNotFound,
		core.CodeAmbiguousStart:    http.StatusConflict,
		core.CodeDeterminismBroken: http.StatusInternalServerError,
		core.CodeInternal:          http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := statusFor(code); got != want {
			t.Errorf("statusFor(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestParseCursor(t *testing.T) {
	cases := []struct {
		name   string
		lastID string
		open   string
		want   stream.Cursor
		bad    bool
	}{
		{name: "empty"},
		{name: "bare sequence", lastID: "7", want: stream.Cursor{Seq: 7}},
		{name: "cursor json", lastID: `{"seq":7,"open":{"m1":3}}`, want: stream.Cursor{Seq: 7, Open: map[string]int64{"m1": 3}}},
		{name: "open query", open: `{"m2":4}`, want: stream.Cursor{Open: map[string]int64{"m2": 4}}},
		{name: "negative sequence", lastID: "-1", bad: true},
		{name: "malformed", lastID: "x", bad: true},
		{name: "malformed json", lastID: "{", bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/v1/runs/r1/stream"
			if tc.open != "" {
				target += "?open=" + tc.open
			}
			r := httptest.NewRequest(http.MethodGet, target, nil)
			if tc.lastID != "" {
				r.Header.Set("Last-Event-ID", tc.lastID)
			}
			got, err := parseCursor(r)
			if tc.bad {
				if core.CodeOf(err) != core.CodeInvalidArgument {
					t.Fatalf("err = %v, want invalid_argument", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCursor: %v", err)
			}
			if got.Seq != tc.want.Seq || len(got.Open) != len(tc.want.Open) {
				t.Fatalf("cursor = %+v, want %+v", got, tc.want)
			}
			for id, dseq := range tc.want.Open {
				if got.Open[id] != dseq {
					t.Fatalf("open[%s] = %d, want %d", id, got.Open[id], dseq)
				}
			}
		})
	}
}

// fakeAttachment replays frames then reports a detach.
type fakeAttachment struct {
	frames []stream.Frame
	i      int
	cursor stream.Cursor
}

func (f *fakeAttachment) Next(context.Context) (stream.Frame, error) {
	if f.i >= len(f.frames) {
		return stream.Frame{}, stream.ErrDetached
	}
	frame := f.frames[f.i]
	f.i++
	switch {
	case frame.Durable != nil && int64(frame.Durable.Seq) > f.cursor.Seq:
		f.cursor.Seq = int64(frame.Durable.Seq)
	case frame.Snapshot != nil && frame.Snapshot.DSeq > f.cursor.Open[frame.Snapshot.MessageID]:
		f.cursor.Open[frame.Snapshot.MessageID] = frame.Snapshot.DSeq
	case frame.Delta != nil && frame.Delta.DSeq > f.cursor.Open[frame.Delta.MessageID]:
		f.cursor.Open[frame.Delta.MessageID] = frame.Delta.DSeq
	}
	return frame, nil
}

func (f *fakeAttachment) Cursor() stream.Cursor { return f.cursor }
func (f *fakeAttachment) Close() error          { return nil }

type fakeStreams struct {
	attachment Attachment
	gotCursor  stream.Cursor
	gotRun     string
}

func (f *fakeStreams) Attach(_ context.Context, runID string, cursor stream.Cursor) (Attachment, error) {
	f.gotRun, f.gotCursor = runID, cursor
	return f.attachment, nil
}

func durableEvent(typ string, seq uint64) eventlog.Event {
	return eventlog.Event{RunID: "r1", Seq: seq, Type: typ, Data: json.RawMessage(`{}`)}
}

func TestStreamWritesNativeEventsAndStopsAtTheEnd(t *testing.T) {
	streams := &fakeStreams{attachment: &fakeAttachment{cursor: stream.Cursor{Open: map[string]int64{}}, frames: []stream.Frame{
		{Durable: func() *eventlog.Event { e := durableEvent("run.started", 1); return &e }()},
		{Delta: &stream.Delta{RunID: "r1", MessageID: "m1", DSeq: 1, Kind: "text", Text: "hi"}},
		{Durable: func() *eventlog.Event { e := durableEvent("run.completed", 2); return &e }()},
	}}}
	s, err := New(Options{Runs: &leakyRuns{}, Streams: streams, Authenticator: AuthenticatorFunc(acmePrincipal)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/runs/r1/stream", nil)
	r.Header.Set("Authorization", "Bearer "+acmeToken)
	r.Header.Set("Last-Event-ID", "3")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{"event: durable", "event: delta", `"text":"hi"`, `"type":"run.completed"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream lacks %q:\n%s", want, body)
		}
	}
	if streams.gotCursor.Seq != 3 {
		t.Fatalf("cursor = %+v, want seq 3", streams.gotCursor)
	}
	// The terminal event ends the response; nothing after it.
	if strings.Count(body, "event: durable") != 2 {
		t.Fatalf("durable events = %d, want 2:\n%s", strings.Count(body, "event: durable"), body)
	}
}

func TestStreamWithoutAnAttachmentIsUnsupported(t *testing.T) {
	s, err := New(Options{Runs: &leakyRuns{}, Authenticator: AuthenticatorFunc(acmePrincipal)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := request(t, s, http.MethodGet, "/v1/runs/r1/stream", acmeToken)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
}

func TestAGUIMapsTheNativeEvents(t *testing.T) {
	started := durableEvent("run.started", 1)
	name, body, ok := aguiEvent(stream.Frame{Durable: &started}, nil)
	if !ok || name != aguiRunStarted || !strings.Contains(string(body), "RUN_STARTED") {
		t.Fatalf("run.started -> %q %s", name, body)
	}
	step := durableEvent("node.started", 2)
	name, _, _ = aguiEvent(stream.Frame{Durable: &step}, nil)
	if name != aguiStepStarted {
		t.Fatalf("node.started -> %q", name)
	}
	text := stream.Frame{Delta: &stream.Delta{MessageID: "m1", DSeq: 1, Kind: "text", Text: "hi"}}
	name, body, _ = aguiEvent(text, nil)
	if name != aguiTextContent || !strings.Contains(string(body), `"delta":"hi"`) {
		t.Fatalf("text delta -> %q %s", name, body)
	}
	reset := stream.Frame{Delta: &stream.Delta{MessageID: "m1", DSeq: 2, Kind: "reset", Attempt: 2}}
	name, _, _ = aguiEvent(reset, nil)
	if name != aguiCustom {
		t.Fatalf("reset -> %q", name)
	}
	snap := stream.Frame{Snapshot: &stream.Snapshot{MessageID: "m1", Text: "so far", DSeq: 4}}
	name, body, _ = aguiEvent(snap, nil)
	if name != aguiMessagesSnap || !strings.Contains(string(body), "so far") {
		t.Fatalf("snapshot -> %q %s", name, body)
	}
}
