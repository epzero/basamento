package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/worker"
)

type loopTransport struct {
	mu        sync.Mutex
	leased    []worker.Invocation
	resumes   []string
	finishes  []finishCall
	releases  []string
	heartbeat worker.HeartbeatAnswer
	beatErr   error
	starting  chan string
	finished  chan string
	left      chan struct{}
	lastLease worker.LeaseRequest
}

type finishCall struct {
	ref   string
	state string
	code  core.Code
	cost  int64
}

func newLoopTransport() *loopTransport {
	return &loopTransport{
		starting: make(chan string, 16),
		finished: make(chan string, 16),
		left:     make(chan struct{}, 1),
	}
}

func (t *loopTransport) Join(context.Context, worker.JoinRequest) (worker.Worker, error) {
	return worker.Worker{ID: "w1", Credential: "c1"}, nil
}
func (t *loopTransport) Lease(_ context.Context, req worker.LeaseRequest) ([]worker.Invocation, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastLease = req
	out := t.leased
	t.leased = nil
	return out, nil
}
func (t *loopTransport) Starting(_ context.Context, _, _, ref string) error {
	select {
	case t.starting <- ref:
	default:
	}
	return nil
}
func (t *loopTransport) Started(context.Context, string, string, string, string) error {
	return nil
}
func (t *loopTransport) Events(context.Context, string, string, string, []worker.Event) error {
	return nil
}
func (t *loopTransport) Finish(_ context.Context, _, _, ref, state string, _ json.RawMessage, code core.Code, cost int64) error {
	t.mu.Lock()
	t.finishes = append(t.finishes, finishCall{ref, state, code, cost})
	t.mu.Unlock()
	select {
	case t.finished <- ref:
	default:
	}
	return nil
}
func (t *loopTransport) Release(_ context.Context, _, _, ref string, _ bool, _ time.Time) error {
	t.mu.Lock()
	t.releases = append(t.releases, ref)
	t.mu.Unlock()
	return nil
}
func (t *loopTransport) Resume(_ context.Context, _, _, ref string) error {
	t.mu.Lock()
	t.resumes = append(t.resumes, ref)
	t.mu.Unlock()
	return nil
}
func (t *loopTransport) Heartbeat(context.Context, string, string, int) (worker.HeartbeatAnswer, error) {
	return t.heartbeat, t.beatErr
}
func (t *loopTransport) Leave(context.Context, string, string) error {
	select {
	case t.left <- struct{}{}:
	default:
	}
	return nil
}

type loopProvider struct {
	mu      sync.Mutex
	starts  int
	cancels int
	started chan struct{}
	result  func(context.Context) (model.Result, error)
}

func (p *loopProvider) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{{ID: "m"}}, nil
}
func (p *loopProvider) Start(_ context.Context, r model.Request) (model.Handle, error) {
	p.mu.Lock()
	p.starts++
	p.mu.Unlock()
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	return model.Handle{ID: "h1", ClientRef: r.ClientRef}, nil
}
func (p *loopProvider) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	return nil, nil
}
func (p *loopProvider) Result(ctx context.Context, _ model.Handle) (model.Result, error) {
	if p.result != nil {
		return p.result(ctx)
	}
	return model.Result{State: model.StateSucceeded}, nil
}
func (p *loopProvider) Cancel(context.Context, model.Handle) error {
	p.mu.Lock()
	p.cancels++
	p.mu.Unlock()
	return nil
}
func (p *loopProvider) Lookup(context.Context, string) (*model.Handle, error) { return nil, nil }

func loopClient(t *testing.T, tr *loopTransport, j *worker.Journal) *worker.Client {
	t.Helper()
	c := worker.NewClient(tr, j)
	if _, err := c.Join(context.Background(), worker.JoinRequest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWorkerLoopActsOnHeartbeatCancelWhileAnInvocationRuns(t *testing.T) {
	tr := newLoopTransport()
	tr.leased = []worker.Invocation{{ClientRef: "r1", Model: "m", Units: 1, Request: json.RawMessage(`{}`)}}
	canceled := make(chan struct{})
	p := &loopProvider{started: make(chan struct{}, 1), result: func(ctx context.Context) (model.Result, error) {
		<-ctx.Done()
		close(canceled)
		return model.Result{State: model.StateCancelled}, nil
	}}
	j, err := worker.OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	beats := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	client := loopClient(t, tr, j)
	loop := &workerLoop{
		client: client, runner: &worker.Runner{Client: client, Providers: map[string]model.Invoker{"m": p}},
		capacity: 1, beats: beats,
	}
	done := make(chan error, 1)
	go func() { done <- loop.serve(ctx) }()

	<-tr.starting
	<-p.started
	tr.mu.Lock()
	tr.heartbeat = worker.HeartbeatAnswer{Cancel: []string{"r1"}}
	tr.mu.Unlock()
	beats <- time.Time{}
	<-canceled
	<-tr.finished
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.finishes) != 1 || tr.finishes[0].state != string(model.StateCancelled) {
		t.Fatalf("finishes %+v, want one cancelled", tr.finishes)
	}
}

func TestWorkerLoopDrainsAFinishedInvocationOnSignal(t *testing.T) {
	tr := newLoopTransport()
	tr.leased = []worker.Invocation{{ClientRef: "r1", Model: "m", Units: 1, Request: json.RawMessage(`{}`)}}
	release := make(chan struct{})
	p := &loopProvider{result: func(context.Context) (model.Result, error) {
		<-release
		return model.Result{State: model.StateSucceeded}, nil
	}}
	j, err := worker.OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	loop := &workerLoop{client: loopClient(t, tr, j), capacity: 1}
	loop.runner = &worker.Runner{Client: loop.client, Providers: map[string]model.Invoker{"m": p}}
	done := make(chan error, 1)
	go func() { done <- loop.serve(ctx) }()

	<-tr.starting
	cancel() // SIGTERM while the invocation runs
	close(release)
	<-tr.finished
	<-tr.left
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.finishes) != 1 || tr.finishes[0].state != string(model.StateSucceeded) {
		t.Fatalf("finishes %+v, want the completed result", tr.finishes)
	}
	if len(tr.releases) != 0 {
		t.Fatalf("released %v, want the invocation finished", tr.releases)
	}
}

func TestWorkerLoopReplaysPendingLeasesAndResults(t *testing.T) {
	tr := newLoopTransport()
	j, err := worker.OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if err := j.Record(worker.JournalEntry{Kind: "lease", ClientRef: "r1", Model: "m", Attempt: 1, Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(worker.JournalEntry{Kind: "result", ClientRef: "r2", State: string(model.StateSucceeded),
		Result: json.RawMessage(`{"ok":true}`), CostMpts: 3}); err != nil {
		t.Fatal(err)
	}
	p := &loopProvider{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop := &workerLoop{client: loopClient(t, tr, j), capacity: 1}
	loop.runner = &worker.Runner{Client: loop.client, Providers: map[string]model.Invoker{"m": p}}
	done := make(chan error, 1)
	go func() { done <- loop.serve(ctx) }()

	for _, want := range []string{"r1", "r2"} {
		if got := <-tr.finished; got != want {
			t.Fatalf("finished %q, want %q", got, want)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.resumes) != 1 || tr.resumes[0] != "r1" {
		t.Fatalf("resumes %v, want r1", tr.resumes)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.starts != 1 {
		t.Fatalf("provider started %d times, want only the resumed lease", p.starts)
	}
	pending, err := j.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending %+v after replay, want none", pending)
	}
}

func TestWorkerLoopTreatsLeaseLostAsFatal(t *testing.T) {
	tr := newLoopTransport()
	tr.beatErr = core.NewError(worker.CodeLeaseLost, "lease lost", nil)
	j, err := worker.OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	beats := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop := &workerLoop{client: loopClient(t, tr, j), capacity: 1, beats: beats}
	loop.runner = &worker.Runner{Client: loop.client}
	done := make(chan error, 1)
	go func() { done <- loop.serve(ctx) }()
	beats <- time.Time{}
	if err := <-done; err == nil {
		t.Fatal("serve returned nil after a lost lease")
	}
}

func TestWorkerLoopDrainsOnTheHeartbeatAnswer(t *testing.T) {
	tr := newLoopTransport()
	tr.heartbeat = worker.HeartbeatAnswer{Drain: true}
	j, err := worker.OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	beats := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := loopClient(t, tr, j)
	loop := &workerLoop{
		client: client, runner: &worker.Runner{Client: client},
		capacity: 1, beats: beats,
	}
	done := make(chan error, 1)
	go func() { done <- loop.serve(ctx) }()

	beats <- time.Time{}
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
	<-tr.left
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.leased) != 0 {
		t.Fatalf("leased %+v after the drain order", tr.leased)
	}
}

func TestWorkerLoopOnceLeavesWhenThereIsNoWork(t *testing.T) {
	tr := newLoopTransport()
	j, err := worker.OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	client := loopClient(t, tr, j)
	loop := &workerLoop{
		client: client, runner: &worker.Runner{Client: client},
		capacity: 1, once: true,
	}
	if err := loop.serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	<-tr.left
}

func TestWorkerLoopAsksForItsWarmModels(t *testing.T) {
	tr := newLoopTransport()
	j, err := worker.OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	client := loopClient(t, tr, j)
	loop := &workerLoop{
		client: client, capacity: 1, once: true,
		warm: []string{"m1", "m2"}, beats: make(chan time.Time),
	}
	if err := loop.serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.lastLease.WarmModels) != 2 || tr.lastLease.WarmModels[0] != "m1" || tr.lastLease.WarmModels[1] != "m2" {
		t.Fatalf("lease asked for warm models %v, want m1, m2", tr.lastLease.WarmModels)
	}
	if tr.lastLease.Wait != 0 {
		t.Fatalf("--once waited %s, want no long poll", tr.lastLease.Wait)
	}
}

func TestWorkerLoopOnceRunsAtMostOneInvocation(t *testing.T) {
	tr := newLoopTransport()
	tr.leased = []worker.Invocation{
		{ClientRef: "r1", Model: "m", Units: 1, Request: json.RawMessage(`{}`)},
		{ClientRef: "r2", Model: "m", Units: 1, Request: json.RawMessage(`{}`)},
	}
	j, err := worker.OpenJournal(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	client := loopClient(t, tr, j)
	loop := &workerLoop{
		client: client, capacity: 2, once: true,
		runner: &worker.Runner{Client: client, Providers: map[string]model.Invoker{"m": &loopProvider{}}},
	}
	if err := loop.serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.finishes) != 1 || tr.finishes[0].ref != "r1" {
		t.Fatalf("finished %+v, want only r1", tr.finishes)
	}
	if len(tr.releases) != 1 || tr.releases[0] != "r2" {
		t.Fatalf("released %v, want r2 handed back unused", tr.releases)
	}
}
