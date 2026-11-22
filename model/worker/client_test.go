package worker

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

type finishCall struct {
	clientRef string
	state     string
	result    json.RawMessage
	code      core.Code
	cost      int64
}

type fakeTransport struct {
	mu           sync.Mutex
	order        []string
	events       [][]Event
	finishes     []finishCall
	releases     []bool
	resumes      []string
	started      []string
	finishErr    error
	resumeErr    error
	heartbeat    HeartbeatAnswer
	heartbeatErr error
	leased       []Invocation
	leaveErr     error
}

func (f *fakeTransport) record(s string) {
	f.mu.Lock()
	f.order = append(f.order, s)
	f.mu.Unlock()
}

func (f *fakeTransport) Join(context.Context, JoinRequest) (Worker, error) {
	return Worker{ID: "w1", Credential: "c1"}, nil
}
func (f *fakeTransport) Lease(context.Context, LeaseRequest) ([]Invocation, error) {
	return f.leased, nil
}
func (f *fakeTransport) Starting(context.Context, string, string, string) error {
	f.record("starting")
	return nil
}
func (f *fakeTransport) Started(_ context.Context, _, _, _, handle string) error {
	f.mu.Lock()
	f.started = append(f.started, handle)
	f.mu.Unlock()
	f.record("started")
	return nil
}
func (f *fakeTransport) Events(_ context.Context, _, _, _ string, events []Event) error {
	f.mu.Lock()
	f.events = append(f.events, events)
	f.mu.Unlock()
	f.record("events")
	return nil
}
func (f *fakeTransport) Finish(_ context.Context, _, _, clientRef, state string, result json.RawMessage, code core.Code, cost int64) error {
	f.mu.Lock()
	f.finishes = append(f.finishes, finishCall{clientRef, state, result, code, cost})
	err := f.finishErr
	f.mu.Unlock()
	f.record("finish")
	return err
}
func (f *fakeTransport) Release(_ context.Context, _, _, _ string, unused bool, _ time.Time) error {
	f.mu.Lock()
	f.releases = append(f.releases, unused)
	f.mu.Unlock()
	f.record("release")
	return nil
}
func (f *fakeTransport) Resume(_ context.Context, _, _, clientRef string) error {
	f.mu.Lock()
	f.resumes = append(f.resumes, clientRef)
	err := f.resumeErr
	f.mu.Unlock()
	f.record("resume")
	return err
}
func (f *fakeTransport) Heartbeat(context.Context, string, string, int) (HeartbeatAnswer, error) {
	f.record("heartbeat")
	return f.heartbeat, f.heartbeatErr
}
func (f *fakeTransport) Leave(context.Context, string, string) error {
	f.record("leave")
	return f.leaveErr
}

type fakeProvider struct {
	mu          sync.Mutex
	startCalls  int
	got         model.Request
	handle      model.Handle
	events      chan model.Event
	startErr    error
	result      func(context.Context) (model.Result, error)
	cancelCalls int
}

func (p *fakeProvider) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{{ID: "m"}}, nil
}
func (p *fakeProvider) Start(_ context.Context, r model.Request) (model.Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.startCalls++
	p.got = r
	if p.startErr != nil {
		return model.Handle{}, p.startErr
	}
	if p.handle.ID == "" {
		p.handle = model.Handle{ID: "h1", ClientRef: r.ClientRef}
	}
	return p.handle, nil
}
func (p *fakeProvider) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	return p.events, nil
}
func (p *fakeProvider) Result(ctx context.Context, _ model.Handle) (model.Result, error) {
	if p.result != nil {
		return p.result(ctx)
	}
	return model.Result{State: model.StateSucceeded}, nil
}
func (p *fakeProvider) Cancel(context.Context, model.Handle) error {
	p.mu.Lock()
	p.cancelCalls++
	p.mu.Unlock()
	return nil
}
func (p *fakeProvider) Lookup(context.Context, string) (*model.Handle, error) { return nil, nil }

func TestRunnerRebuildsTheWholeRequestAndForwardsEvents(t *testing.T) {
	ctx := context.Background()
	tr := &fakeTransport{}
	j, err := OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	client := NewClient(tr, j)
	if _, err := client.Join(ctx, JoinRequest{}); err != nil {
		t.Fatal(err)
	}
	events := make(chan model.Event, 3)
	events <- model.Event{Kind: model.EventText, Text: "hello"}
	events <- model.Event{Kind: model.EventAudio, Audio: []byte{1, 2}, Attempt: 2}
	events <- model.Event{Kind: model.EventProgress, Progress: 0.5, Data: json.RawMessage(`{"step":3}`)}
	close(events)
	p := &fakeProvider{events: events}
	req := model.Request{
		Model: "m", Op: model.CapChat, Messages: json.RawMessage(`[{"role":"user"}]`),
		Params: json.RawMessage(`{"n":2}`), ClientRef: "r1",
	}
	raw, err := encodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Client: client, Providers: map[string]model.Invoker{"m": p}}
	if err := runner.Run(ctx, Invocation{ClientRef: "r1", Model: "m", Request: raw}); err != nil {
		t.Fatal(err)
	}
	if p.got.Op != model.CapChat || string(p.got.Messages) != string(req.Messages) ||
		string(p.got.Params) != string(req.Params) || p.got.ClientRef != "r1" {
		t.Fatalf("provider got %+v, want the queued request", p.got)
	}
	tr.mu.Lock()
	gotEvents := append([][]Event(nil), tr.events...)
	order := append([]string(nil), tr.order...)
	finishes := append([]finishCall(nil), tr.finishes...)
	tr.mu.Unlock()
	if len(gotEvents) != 3 {
		t.Fatalf("reported %d event batches, want 3", len(gotEvents))
	}
	for i, batch := range gotEvents {
		if len(batch) != 1 || batch[0].Seq != int64(i+1) {
			t.Fatalf("batch %d %+v, want one event with monotonic seq %d", i, batch, i+1)
		}
	}
	if gotEvents[0][0].Text != "hello" || len(gotEvents[1][0].Audio) != 2 ||
		gotEvents[1][0].Attempt != 2 || gotEvents[2][0].Progress != 0.5 ||
		string(gotEvents[2][0].Data) != `{"step":3}` {
		t.Fatalf("events lost their payload: %+v", gotEvents)
	}
	if len(finishes) != 1 || finishes[0].state != string(model.StateSucceeded) {
		t.Fatalf("finishes %+v, want one success", finishes)
	}
	if order[len(order)-1] != "finish" {
		t.Fatalf("order %v, want finish after the events", order)
	}
}

func TestRunnerRunResumedDoesNotStartTheProviderAgain(t *testing.T) {
	ctx := context.Background()
	tr := &fakeTransport{}
	client := NewClient(tr, nil)
	if _, err := client.Join(ctx, JoinRequest{}); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	runner := &Runner{Client: client, Providers: map[string]model.Invoker{"m": p}}
	if err := runner.RunResumed(ctx, Invocation{ClientRef: "r1", Model: "m"}, "h-old"); err != nil {
		t.Fatal(err)
	}
	if p.startCalls != 0 {
		t.Fatalf("provider started %d times, want reattach only", p.startCalls)
	}
	tr.mu.Lock()
	started := append([]string(nil), tr.started...)
	tr.mu.Unlock()
	if len(started) != 1 || started[0] != "h-old" {
		t.Fatalf("reported handles %v, want h-old", started)
	}
}

func TestRunnerCancelsTheProviderWhenTheLeaseIsGone(t *testing.T) {
	ctx := context.Background()
	tr := &fakeTransport{finishErr: errLeaseLost("r1")}
	client := NewClient(tr, nil)
	if _, err := client.Join(ctx, JoinRequest{}); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	runner := &Runner{Client: client, Providers: map[string]model.Invoker{"m": p}}
	err := runner.Run(ctx, Invocation{ClientRef: "r1", Model: "m", Request: json.RawMessage(`{}`)})
	if core.CodeOf(err) != CodeLeaseLost {
		t.Fatalf("got %v, want lease_lost", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancelCalls != 1 {
		t.Fatalf("provider cancelled %d times, want 1", p.cancelCalls)
	}
}

func TestClientFinishJournalsTheOutcomeBeforeTheTransportCall(t *testing.T) {
	ctx := context.Background()
	tr := &fakeTransport{finishErr: errors.New("network down")}
	j, err := OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	client := NewClient(tr, j)
	if _, err := client.Join(ctx, JoinRequest{}); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"ok":true}`)
	if err := client.Finish(ctx, "r1", string(model.StateSucceeded), result, "", 9); err == nil {
		t.Fatal("Finish succeeded while the transport was down")
	}
	pending, err := j.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Kind != "result" || pending[0].State != string(model.StateSucceeded) ||
		string(pending[0].Result) != string(result) || pending[0].CostMpts != 9 {
		t.Fatalf("pending %+v, want the undelivered outcome", pending)
	}
	tr.mu.Lock()
	tr.finishErr = nil
	tr.mu.Unlock()
	if err := client.Finish(ctx, "r1", string(model.StateSucceeded), result, "", 9); err != nil {
		t.Fatal(err)
	}
	pending, err = j.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending %+v after redelivery, want none", pending)
	}
}

func TestStoreEventsKeepsPayloadAndMonotonicSequence(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	w := joinWorker(t, s, "p", pool.Token, "m", 1, false)
	enqueue(t, s, "r1", "p", "acme", "m", ClassNormal, 0)
	lease(t, s, w, 1)
	if err := s.Starting(ctx, w.ID, w.Credential, "r1"); err != nil {
		t.Fatal(err)
	}
	// The second and third batches deliberately restart at 1, as a new
	// attempt or a restarted worker does.
	if err := s.Events(ctx, w.ID, w.Credential, "r1", []Event{{Seq: 1, Kind: "text", Text: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Events(ctx, w.ID, w.Credential, "r1", []Event{{
		Seq: 1, Kind: "audio", Audio: []byte{7}, Progress: 0.25, Attempt: 2,
		Data: json.RawMessage(`{"x":1}`),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Events(ctx, w.ID, w.Credential, "r1", []Event{{Seq: 1, Kind: "progress", Progress: 0.5}}); err != nil {
		t.Fatal(err)
	}
	if err := appendReset(ctx, s.db.Execer(), "r1"); err != nil {
		t.Fatal(err)
	}
	events, err := s.EventsAfter(ctx, "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("%d events, want 4: %+v", len(events), events)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, want %d", i, e.Seq, i+1)
		}
	}
	if events[0].Text != "a" || len(events[1].Audio) != 1 || events[1].Attempt != 2 ||
		events[1].Progress != 0.25 || string(events[1].Data) != `{"x":1}` ||
		events[2].Progress != 0.5 || events[3].Kind != "reset" {
		t.Fatalf("events lost their payload: %+v", events)
	}
}

func runOne(t *testing.T, p *fakeProvider) (*fakeTransport, error) {
	t.Helper()
	ctx := context.Background()
	tr := &fakeTransport{}
	j, err := OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	client := NewClient(tr, j)
	if _, err := client.Join(ctx, JoinRequest{}); err != nil {
		t.Fatal(err)
	}
	raw, err := encodeRequest(model.Request{Model: "m", ClientRef: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Client: client, Providers: map[string]model.Invoker{"m": p}}
	return tr, runner.Run(ctx, Invocation{ClientRef: "r1", Model: "m", Request: raw})
}

func TestRunnerFinishesADefinitiveProviderFailure(t *testing.T) {
	p := &fakeProvider{result: func(context.Context) (model.Result, error) {
		return model.Result{}, core.NewError(core.CodeInvalidArgument, "bad request", nil)
	}}
	tr, err := runOne(t, p)
	if err == nil {
		t.Fatal("run succeeded, want the provider failure")
	}
	if len(tr.finishes) != 1 {
		t.Fatalf("finished %d times, want once", len(tr.finishes))
	}
	got := tr.finishes[0]
	if got.state != string(model.StateFailed) || got.code != core.CodeInvalidArgument || got.cost != 0 {
		t.Fatalf("finish %+v, want failed invalid_argument at no cost", got)
	}
	if len(tr.releases) != 0 {
		t.Fatalf("released %v, want a definitive failure finished instead", tr.releases)
	}
}

func TestRunnerReleasesAnAmbiguousProviderFailure(t *testing.T) {
	p := &fakeProvider{result: func(context.Context) (model.Result, error) {
		return model.Result{}, &core.Error{Code: core.CodeTimeout, Retryable: true}
	}}
	tr, err := runOne(t, p)
	if err == nil {
		t.Fatal("run succeeded, want the provider failure")
	}
	if len(tr.finishes) != 0 {
		t.Fatalf("finished %+v, want no terminal report for an ambiguous failure", tr.finishes)
	}
	if len(tr.releases) != 1 || tr.releases[0] {
		t.Fatalf("released %v, want one started (in-doubt) release", tr.releases)
	}
}

func TestRunnerFinishesADefinitiveStartFailure(t *testing.T) {
	p := &fakeProvider{startErr: core.NewError(core.CodeUnsupported, "no", nil)}
	tr, err := runOne(t, p)
	if err == nil {
		t.Fatal("run succeeded, want the start failure")
	}
	if len(tr.finishes) != 1 || tr.finishes[0].code != core.CodeUnsupported {
		t.Fatalf("finish %+v, want failed unsupported", tr.finishes)
	}
	if len(tr.releases) != 0 {
		t.Fatalf("released %v, want the refusal finished", tr.releases)
	}
}

func TestRunnerCarriesAFailedResultsCodeAndCost(t *testing.T) {
	p := &fakeProvider{result: func(context.Context) (model.Result, error) {
		return model.Result{
			State:    model.StateFailed,
			Error:    json.RawMessage(`{"code":"permission_denied","message":"no"}`),
			CostMpts: 7,
		}, nil
	}}
	tr, err := runOne(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.finishes) != 1 {
		t.Fatalf("finished %d times, want once", len(tr.finishes))
	}
	got := tr.finishes[0]
	if got.state != string(model.StateFailed) || got.code != core.CodePermissionDenied || got.cost != 7 {
		t.Fatalf("finish %+v, want failed permission_denied charged 7", got)
	}
}
