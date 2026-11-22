package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// Transport is the worker's side of basamento.worker.v1: short calls, each
// naming the worker and the lease it is about, so a dropped connection costs a
// short delay and never an invocation (ADR-0012 §4). The platform implements
// the same calls over gRPC.
type Transport interface {
	Join(ctx context.Context, req JoinRequest) (Worker, error)
	Lease(ctx context.Context, req LeaseRequest) ([]Invocation, error)
	Starting(ctx context.Context, workerID, credential, clientRef string, attempt int) error
	Started(ctx context.Context, workerID, credential, clientRef, providerHandle string, attempt int) error
	Events(ctx context.Context, workerID, credential, clientRef string, attempt int, events []Event) error
	Finish(ctx context.Context, workerID, credential, clientRef, state string, result json.RawMessage, code core.Code, cost int64, attempt int) error
	Release(ctx context.Context, workerID, credential, clientRef string, unused bool, notBefore time.Time, attempt int) error
	Resume(ctx context.Context, workerID, credential, clientRef string, attempt int) error
	Heartbeat(ctx context.Context, workerID, credential string, freeUnits int) (HeartbeatAnswer, error)
	Leave(ctx context.Context, workerID, credential string) error
}

// Client is a connected worker: it holds the worker id and credential a join
// yielded, keeps its leases in the journal and takes work from its pool.
type Client struct {
	transport Transport
	journal   *Journal
	worker    Worker
	state     State
}

// NewClient returns a client over transport and journal. journal may be nil
// for a worker that cannot keep one; the ADR requires a writable data
// directory, so the command always passes one.
func NewClient(transport Transport, journal *Journal) *Client {
	return &Client{transport: transport, journal: journal, state: StateLeft}
}

// Join exchanges the pool token for the worker id and credential and records
// the descriptors the pool now keeps.
func (c *Client) Join(ctx context.Context, req JoinRequest) (Worker, error) {
	if req.ProtocolVersion == "" {
		req.ProtocolVersion = ProtocolVersion
	}
	w, err := c.transport.Join(ctx, req)
	if err != nil {
		return Worker{}, err
	}
	c.worker = w
	c.state = StateActive
	return w, nil
}

// Worker returns the joined worker.
func (c *Client) Worker() Worker { return c.worker }

// State returns the worker's state as the platform last reported it.
func (c *Client) State() State { return c.state }

// Journal returns the client's journal.
func (c *Client) Journal() *Journal { return c.journal }

// Pending returns leases the journal says are undelivered, for a worker that
// crashed or restarted to resume (ADR-0012 §7).
func (c *Client) Pending() ([]JournalEntry, error) {
	if c.journal == nil {
		return nil, nil
	}
	return c.journal.Pending()
}

// LeaseOnce asks for work with the given free units and records each lease in
// the journal before the caller starts it.
func (c *Client) LeaseOnce(ctx context.Context, free int, warm []string, timeLeft, wait time.Duration) ([]Invocation, error) {
	leased, err := c.transport.Lease(ctx, LeaseRequest{
		WorkerID:   c.worker.ID,
		Credential: c.worker.Credential,
		FreeUnits:  free,
		WarmModels: warm,
		TimeLeft:   timeLeft,
		Wait:       wait,
	})
	if err != nil {
		return nil, err
	}
	for _, inv := range leased {
		if c.journal == nil {
			continue
		}
		if err := c.journal.Record(JournalEntry{
			Kind: "lease", ClientRef: inv.ClientRef, Model: inv.Model, Attempt: inv.Attempt,
			LeaseExpiresUnixMS: inv.LeaseExpires.UnixMilli(), Request: inv.Request,
		}); err != nil {
			return nil, err
		}
	}
	return leased, nil
}

// Starting tells the platform the worker is about to call its provider.
func (c *Client) Starting(ctx context.Context, clientRef string, attempt int) error {
	return c.transport.Starting(ctx, c.worker.ID, c.worker.Credential, clientRef, attempt)
}

// Started reports the provider's handle.
func (c *Client) Started(ctx context.Context, clientRef, handle string, attempt int) error {
	if c.journal != nil {
		if err := c.journal.Record(JournalEntry{Kind: "handle", ClientRef: clientRef, Attempt: attempt, ProviderHandle: handle}); err != nil {
			return err
		}
	}
	return c.transport.Started(ctx, c.worker.ID, c.worker.Credential, clientRef, handle, attempt)
}

// Events reports partial output for a lease.
func (c *Client) Events(ctx context.Context, clientRef string, attempt int, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	return c.transport.Events(ctx, c.worker.ID, c.worker.Credential, clientRef, attempt, events)
}

// Resume takes back an in-doubt invocation inside its grace period
// (REQ-MODEL-023).
func (c *Client) Resume(ctx context.Context, clientRef string, attempt int) error {
	return c.transport.Resume(ctx, c.worker.ID, c.worker.Credential, clientRef, attempt)
}

// Finish reports a result and marks the journal entry delivered. The outcome
// is journaled before the transport call, so a failed call leaves it for a
// restart to redeliver (ADR-0012 §7).
func (c *Client) Finish(ctx context.Context, clientRef, state string, result json.RawMessage, code core.Code, cost int64, attempt int) error {
	if c.journal != nil {
		if err := c.journal.Record(JournalEntry{
			Kind: "result", ClientRef: clientRef, Attempt: attempt, State: state,
			Result: result, ErrorCode: string(code), CostMpts: cost,
		}); err != nil {
			return err
		}
	}
	if err := c.transport.Finish(ctx, c.worker.ID, c.worker.Credential, clientRef, state, result, code, cost, attempt); err != nil {
		return err
	}
	if c.journal != nil {
		if err := c.journal.Record(JournalEntry{Kind: "done", ClientRef: clientRef, Attempt: attempt}); err != nil {
			return err
		}
	}
	return nil
}

// Release gives a lease back. Unused says the worker never started the
// invocation.
func (c *Client) Release(ctx context.Context, clientRef string, unused bool, notBefore time.Time, attempt int) error {
	if err := c.transport.Release(ctx, c.worker.ID, c.worker.Credential, clientRef, unused, notBefore, attempt); err != nil {
		return err
	}
	if c.journal != nil {
		return c.journal.Record(JournalEntry{Kind: "done", ClientRef: clientRef, Attempt: attempt})
	}
	return nil
}

// Heartbeat keeps the worker present and its leases alive.
func (c *Client) Heartbeat(ctx context.Context, free int) (HeartbeatAnswer, error) {
	answer, err := c.transport.Heartbeat(ctx, c.worker.ID, c.worker.Credential, free)
	if err != nil {
		return HeartbeatAnswer{}, err
	}
	if answer.Credential != "" {
		// The platform rotates the credential on every heartbeat; the next
		// call must present the new one (ADR-0012 §10).
		c.worker.Credential = answer.Credential
	}
	c.state = answer.State
	return answer, nil
}

// Leave is a clean exit.
func (c *Client) Leave(ctx context.Context) error {
	if err := c.transport.Leave(ctx, c.worker.ID, c.worker.Credential); err != nil {
		return err
	}
	c.state = StateLeft
	return nil
}

// Runner executes leased invocations with the providers a worker loaded. It is
// the part `kit worker` runs after joining: Starting, the provider's start,
// Started with its handle, partial output, the result, and the journal around
// all of it.
type Runner struct {
	Client    *Client
	Providers map[string]model.Invoker

	mu      sync.Mutex
	runs    map[string]runHandle
	pending map[string]int
}

type runHandle struct {
	cancel  context.CancelFunc
	attempt int
}

// Run executes one leased invocation and reports its terminal state to the
// platform. A provider that refuses the start definitively finishes the
// invocation as failed with the provider's code; an ambiguous start failure
// releases the lease, which leaves a non-restart-safe model in doubt for this
// worker rather than handing it to another.
func (r *Runner) Run(ctx context.Context, inv Invocation) error {
	return r.run(ctx, inv, "")
}

// RunResumed executes a lease the worker already holds, reattaching to the
// provider handle in its journal instead of starting the invocation again
// (ADR-0012 §7).
func (r *Runner) RunResumed(ctx context.Context, inv Invocation, providerHandle string) error {
	return r.run(ctx, inv, providerHandle)
}

// Cancel asks the run for clientRef to stop: the provider is cancelled and the
// invocation is reported cancelled. attempt names the attempt the cancel is
// for, so a cancel for a superseded attempt never stops the current one. A
// cancel that arrives before the run is tracked is kept and honoured at its
// start. It reports whether a local run matched.
func (r *Runner) Cancel(clientRef string, attempt int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.runs[clientRef]; ok {
		if attempt != 0 && run.attempt != attempt {
			return false
		}
		run.cancel()
		return true
	}
	if r.pending == nil {
		r.pending = map[string]int{}
	}
	r.pending[clientRef] = attempt
	return true
}

func (r *Runner) takePending(clientRef string, attempt int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	got, ok := r.pending[clientRef]
	if !ok || got != 0 && got != attempt {
		return false
	}
	delete(r.pending, clientRef)
	return true
}

func (r *Runner) track(clientRef string, attempt int, cancel context.CancelFunc) func() {
	r.mu.Lock()
	if r.runs == nil {
		r.runs = map[string]runHandle{}
	}
	r.runs[clientRef] = runHandle{cancel: cancel, attempt: attempt}
	// A cancel may have arrived between the lease and this point.
	if got, ok := r.pending[clientRef]; ok && (got == 0 || got == attempt) {
		delete(r.pending, clientRef)
		cancel()
	}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.runs, clientRef)
		r.mu.Unlock()
	}
}

func (r *Runner) run(ctx context.Context, inv Invocation, providerHandle string) error {
	provider := r.providers()[inv.Model]
	if provider == nil {
		_ = r.Client.Release(ctx, inv.ClientRef, true, time.Time{}, inv.Attempt)
		return core.NewError(core.CodeUnsupported,
			"worker loaded no provider for the model", map[string]any{"model": inv.Model})
	}
	req, err := decodeRequest(inv)
	if err != nil {
		_ = r.Client.Finish(ctx, inv.ClientRef, string(model.StateFailed), nil, core.CodeInvalidArgument, 0, inv.Attempt)
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if r.takePending(inv.ClientRef, inv.Attempt) {
		return r.finishCancelled(ctx, inv.ClientRef, inv.Attempt, 0)
	}
	untrack := r.track(inv.ClientRef, inv.Attempt, cancel)
	defer untrack()

	if err := r.Client.Starting(runCtx, inv.ClientRef, inv.Attempt); err != nil {
		if runCtx.Err() != nil {
			return r.finishCancelled(ctx, inv.ClientRef, inv.Attempt, 0)
		}
		// Nothing was started; hand the lease back unused.
		_ = r.Client.Release(context.WithoutCancel(ctx), inv.ClientRef, true, time.Time{}, inv.Attempt)
		return err
	}
	var handle model.Handle
	if providerHandle == "" {
		handle, err = provider.Start(runCtx, req)
		if err != nil {
			if runCtx.Err() != nil {
				return r.finishCancelled(ctx, inv.ClientRef, inv.Attempt, 0)
			}
			if definitiveFailure(err) {
				// The provider refused the call and is not running it:
				// report the failure instead of leaving the invocation to
				// expire as worker_lost.
				_ = r.Client.Finish(ctx, inv.ClientRef, string(model.StateFailed), nil, core.CodeOf(err), 0, inv.Attempt)
			} else {
				_ = r.Client.Release(ctx, inv.ClientRef, false, time.Time{}, inv.Attempt)
			}
			return err
		}
	} else {
		handle = model.Handle{ID: providerHandle, ClientRef: inv.ClientRef}
	}
	if err := r.Client.Started(runCtx, inv.ClientRef, handle.ID, inv.Attempt); err != nil {
		if runCtx.Err() != nil {
			return r.finishCancelled(ctx, inv.ClientRef, inv.Attempt, 0)
		}
		// The provider may be running: leave the lease in doubt rather than
		// start a second paid attempt.
		_ = r.Client.Release(context.WithoutCancel(ctx), inv.ClientRef, false, time.Time{}, inv.Attempt)
		return err
	}

	eventsDone := r.forwardEvents(runCtx, inv.ClientRef, inv.Attempt, provider, handle, cancel)

	type outcome struct {
		result model.Result
		err    error
	}
	results := make(chan outcome, 1)
	go func() {
		res, err := provider.Result(runCtx, handle)
		results <- outcome{result: res, err: err}
	}()

	var res model.Result
	var resErr error
	cancelled := false
	select {
	case out := <-results:
		res, resErr = out.result, out.err
	case <-runCtx.Done():
		if ctx.Err() != nil {
			// The caller is going away; hand the lease back rather than
			// leave it in doubt for the whole grace period.
			_ = provider.Cancel(context.Background(), handle)
			_ = r.Client.Release(context.WithoutCancel(ctx), inv.ClientRef, false, time.Time{}, inv.Attempt)
			return ctx.Err()
		}
		cancelled = true
		_ = provider.Cancel(context.Background(), handle)
		select {
		case out := <-results:
			res, resErr = out.result, out.err
		default:
			res = model.Result{State: model.StateCancelled}
		}
	}
	if eventsDone != nil {
		select {
		case <-eventsDone:
		case <-runCtx.Done():
		}
	}

	reportCtx := ctx
	if ctx.Err() != nil {
		reportCtx = context.WithoutCancel(ctx)
	}
	if cancelled {
		err := r.finishCancelled(reportCtx, inv.ClientRef, inv.Attempt, res.CostMpts)
		if core.CodeOf(err) == CodeLeaseLost {
			_ = provider.Cancel(context.Background(), handle)
		}
		return err
	}
	if resErr != nil {
		if definitiveFailure(resErr) {
			// The provider ended the invocation with a failure: report it
			// with its code and settle the reservation, not worker_lost.
			state := string(model.StateFailed)
			if core.CodeOf(resErr) == core.CodeCancelled {
				state = string(model.StateCancelled)
			}
			_ = r.Client.Finish(reportCtx, inv.ClientRef, state, nil, core.CodeOf(resErr), res.CostMpts, inv.Attempt)
		} else {
			_ = r.Client.Release(reportCtx, inv.ClientRef, false, time.Time{}, inv.Attempt)
		}
		return resErr
	}
	var state string
	code := core.Code("")
	switch res.State {
	case model.StateSucceeded, "":
		state = string(model.StateSucceeded)
	case model.StateFailed:
		state = string(model.StateFailed)
		code = failureCode(res)
	case model.StateCancelled:
		state = string(model.StateCancelled)
		code = core.CodeCancelled
	default:
		// The provider has not ended; an in-doubt lease is the honest
		// answer, so a second paid attempt is never started.
		_ = r.Client.Release(reportCtx, inv.ClientRef, false, time.Time{}, inv.Attempt)
		return core.NewError(core.CodeAmbiguousStart,
			"the provider has not reported a result", map[string]any{"client_ref": inv.ClientRef})
	}
	var raw json.RawMessage
	if len(res.Message) > 0 {
		raw = res.Message
	}
	err = r.Client.Finish(reportCtx, inv.ClientRef, state, raw, code, res.CostMpts, inv.Attempt)
	if core.CodeOf(err) == CodeLeaseLost {
		// The platform already has a result; do not leave the provider
		// running a superseded attempt (ADR-0012 §7).
		_ = provider.Cancel(context.Background(), handle)
	}
	return err
}

// forwardEvents reports the provider's partial output while the invocation
// runs. The sequence is the worker's own and stays monotonic across a run; the
// store also refuses to go backwards. Partial output is best-effort: a failed
// batch does not lose the result, and a lease that is gone cancels the run.
func (r *Runner) forwardEvents(ctx context.Context, clientRef string, attempt int, provider model.Invoker, handle model.Handle, cancel context.CancelFunc) <-chan struct{} {
	events, err := provider.Events(ctx, handle, 0)
	if err != nil || events == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var seq int64
		for e := range events {
			seq++
			if err := r.Client.Events(ctx, clientRef, attempt, []Event{{
				Seq: seq, Kind: string(e.Kind), Text: e.Text, Audio: e.Audio,
				Progress: e.Progress, Attempt: e.Attempt, Data: e.Data,
			}}); err != nil && core.CodeOf(err) == CodeLeaseLost {
				cancel()
				return
			}
		}
	}()
	return done
}

func (r *Runner) finishCancelled(ctx context.Context, clientRef string, attempt int, cost int64) error {
	if ctx.Err() != nil {
		ctx = context.WithoutCancel(ctx)
	}
	return r.Client.Finish(ctx, clientRef, string(model.StateCancelled), nil, core.CodeCancelled, cost, attempt)
}

// CancelProvider cancels a provider handle the worker journaled but can no
// longer report (a superseded attempt).
func (r *Runner) CancelProvider(ctx context.Context, modelID, clientRef, handle string) {
	if handle == "" {
		return
	}
	if p := r.providers()[modelID]; p != nil {
		_ = p.Cancel(ctx, model.Handle{ID: handle, ClientRef: clientRef})
	}
}

func (r *Runner) providers() map[string]model.Invoker {
	if r.Providers != nil {
		return r.Providers
	}
	return map[string]model.Invoker{}
}

// definitiveFailure reports whether err says the invocation ended and is not
// running, so the platform may finish it rather than leave it in doubt.
// Timeouts, unavailable providers, rate limits and vendor errors are
// ambiguous: the provider may still be running and may still charge.
func definitiveFailure(err error) bool {
	var pe *core.Error
	if !errors.As(err, &pe) {
		return false
	}
	if pe.Retryable {
		return false
	}
	switch pe.Code {
	case core.CodeTimeout, core.CodeUnavailable, core.CodeRateLimited,
		core.CodeAmbiguousStart, core.CodeVendor, core.CodeInternal:
		return false
	}
	return true
}

// failureCode reads the platform code a provider recorded in a failed result.
func failureCode(res model.Result) core.Code {
	var f model.Failure
	if len(res.Error) > 0 && json.Unmarshal(res.Error, &f) == nil && f.Code != "" {
		return f.Code
	}
	return core.CodeVendor
}

// LocalTransport is the in-process Transport over a Store, for the embedded
// profile and the gate. It adapts the worker contract to the store's methods.
type LocalTransport struct {
	Store *Store
}

// Join implements Transport.
func (t LocalTransport) Join(ctx context.Context, req JoinRequest) (Worker, error) {
	return t.Store.Join(ctx, req)
}

// Lease implements Transport.
func (t LocalTransport) Lease(ctx context.Context, req LeaseRequest) ([]Invocation, error) {
	return t.Store.Lease(ctx, req)
}

// Starting implements Transport.
func (t LocalTransport) Starting(ctx context.Context, workerID, credential, clientRef string, attempt int) error {
	return t.Store.Starting(ctx, workerID, credential, clientRef, attempt)
}

// Started implements Transport.
func (t LocalTransport) Started(ctx context.Context, workerID, credential, clientRef, providerHandle string, attempt int) error {
	return t.Store.Started(ctx, workerID, credential, clientRef, providerHandle, attempt)
}

// Events implements Transport.
func (t LocalTransport) Events(ctx context.Context, workerID, credential, clientRef string, attempt int, events []Event) error {
	return t.Store.Events(ctx, workerID, credential, clientRef, attempt, events)
}

// Finish implements Transport.
func (t LocalTransport) Finish(ctx context.Context, workerID, credential, clientRef, state string, result json.RawMessage, code core.Code, cost int64, attempt int) error {
	return t.Store.Finish(ctx, workerID, credential, clientRef, state, result, code, cost, attempt)
}

// Release implements Transport.
func (t LocalTransport) Release(ctx context.Context, workerID, credential, clientRef string, unused bool, notBefore time.Time, attempt int) error {
	return t.Store.Release(ctx, workerID, credential, clientRef, unused, notBefore, attempt)
}

// Resume implements Transport.
func (t LocalTransport) Resume(ctx context.Context, workerID, credential, clientRef string, attempt int) error {
	return t.Store.Resume(ctx, workerID, credential, clientRef, attempt)
}

// Heartbeat implements Transport.
func (t LocalTransport) Heartbeat(ctx context.Context, workerID, credential string, freeUnits int) (HeartbeatAnswer, error) {
	return t.Store.Heartbeat(ctx, workerID, credential, freeUnits)
}

// Leave implements Transport.
func (t LocalTransport) Leave(ctx context.Context, workerID, credential string) error {
	return t.Store.Leave(ctx, workerID, credential)
}
