package worker

import (
	"context"
	"encoding/json"
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
	Starting(ctx context.Context, workerID, credential, clientRef string) error
	Started(ctx context.Context, workerID, credential, clientRef, providerHandle string) error
	Finish(ctx context.Context, workerID, credential, clientRef, state string, result json.RawMessage, code core.Code, cost int64) error
	Release(ctx context.Context, workerID, credential, clientRef string, notBefore time.Time) error
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
func (c *Client) LeaseOnce(ctx context.Context, free int, warm []string, timeLeft time.Duration) ([]Invocation, error) {
	leased, err := c.transport.Lease(ctx, LeaseRequest{
		WorkerID:   c.worker.ID,
		Credential: c.worker.Credential,
		FreeUnits:  free,
		WarmModels: warm,
		TimeLeft:   timeLeft,
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
			LeaseExpiresUnixMS: inv.LeaseExpires.UnixMilli(),
		}); err != nil {
			return nil, err
		}
	}
	return leased, nil
}

// Starting tells the platform the worker is about to call its provider.
func (c *Client) Starting(ctx context.Context, clientRef string) error {
	return c.transport.Starting(ctx, c.worker.ID, c.worker.Credential, clientRef)
}

// Started reports the provider's handle.
func (c *Client) Started(ctx context.Context, clientRef, handle string) error {
	if c.journal != nil {
		if err := c.journal.Record(JournalEntry{Kind: "handle", ClientRef: clientRef, ProviderHandle: handle}); err != nil {
			return err
		}
	}
	return c.transport.Started(ctx, c.worker.ID, c.worker.Credential, clientRef, handle)
}

// Finish reports a result and marks the journal entry delivered.
func (c *Client) Finish(ctx context.Context, clientRef, state string, result json.RawMessage, code core.Code, cost int64) error {
	if err := c.transport.Finish(ctx, c.worker.ID, c.worker.Credential, clientRef, state, result, code, cost); err != nil {
		return err
	}
	if c.journal != nil {
		if err := c.journal.Record(JournalEntry{
			Kind: "done", ClientRef: clientRef, Result: result, ErrorCode: string(code), CostMpts: cost,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Release gives a lease back.
func (c *Client) Release(ctx context.Context, clientRef string, notBefore time.Time) error {
	if err := c.transport.Release(ctx, c.worker.ID, c.worker.Credential, clientRef, notBefore); err != nil {
		return err
	}
	if c.journal != nil {
		return c.journal.Record(JournalEntry{Kind: "done", ClientRef: clientRef})
	}
	return nil
}

// Heartbeat keeps the worker present and its leases alive.
func (c *Client) Heartbeat(ctx context.Context, free int) (HeartbeatAnswer, error) {
	answer, err := c.transport.Heartbeat(ctx, c.worker.ID, c.worker.Credential, free)
	if err != nil {
		return HeartbeatAnswer{}, err
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
// Started with its handle, the result, and the journal around all of it.
type Runner struct {
	Client    *Client
	Providers map[string]model.Invoker
}

// Run executes one leased invocation and reports its terminal state to the
// platform. A provider that cannot start reports the failure and the lease is
// released for another worker.
func (r *Runner) Run(ctx context.Context, inv Invocation) error {
	provider := r.providers()[inv.Model]
	if provider == nil {
		_ = r.Client.Release(ctx, inv.ClientRef, time.Time{})
		return core.NewError(core.CodeUnsupported,
			"worker loaded no provider for the model", map[string]any{"model": inv.Model})
	}
	if err := r.Client.Starting(ctx, inv.ClientRef); err != nil {
		return err
	}
	req := model.Request{Model: inv.Model, ClientRef: inv.ClientRef}
	if len(inv.Request) > 0 && string(inv.Request) != "{}" {
		req.Params = inv.Request
	}
	handle, err := provider.Start(ctx, req)
	if err != nil {
		_ = r.Client.Release(ctx, inv.ClientRef, time.Time{})
		return err
	}
	if err := r.Client.Started(ctx, inv.ClientRef, handle.ID); err != nil {
		return err
	}
	result, err := provider.Result(ctx, handle)
	if err != nil {
		_ = r.Client.Release(ctx, inv.ClientRef, time.Time{})
		return err
	}
	state := string(result.State)
	if state == "" {
		state = string(model.StateSucceeded)
	}
	var raw json.RawMessage
	if len(result.Message) > 0 {
		raw = result.Message
	}
	return r.Client.Finish(ctx, inv.ClientRef, state, raw, "", result.CostMpts)
}

func (r *Runner) providers() map[string]model.Invoker {
	if r.Providers != nil {
		return r.Providers
	}
	return map[string]model.Invoker{}
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
func (t LocalTransport) Starting(ctx context.Context, workerID, credential, clientRef string) error {
	return t.Store.Starting(ctx, workerID, credential, clientRef)
}

// Started implements Transport.
func (t LocalTransport) Started(ctx context.Context, workerID, credential, clientRef, providerHandle string) error {
	return t.Store.Started(ctx, workerID, credential, clientRef, providerHandle)
}

// Finish implements Transport.
func (t LocalTransport) Finish(ctx context.Context, workerID, credential, clientRef, state string, result json.RawMessage, code core.Code, cost int64) error {
	return t.Store.Finish(ctx, workerID, credential, clientRef, state, result, code, cost)
}

// Release implements Transport.
func (t LocalTransport) Release(ctx context.Context, workerID, credential, clientRef string, notBefore time.Time) error {
	return t.Store.Release(ctx, workerID, credential, clientRef, notBefore)
}

// Heartbeat implements Transport.
func (t LocalTransport) Heartbeat(ctx context.Context, workerID, credential string, freeUnits int) (HeartbeatAnswer, error) {
	return t.Store.Heartbeat(ctx, workerID, credential, freeUnits)
}

// Leave implements Transport.
func (t LocalTransport) Leave(ctx context.Context, workerID, credential string) error {
	return t.Store.Leave(ctx, workerID, credential)
}
