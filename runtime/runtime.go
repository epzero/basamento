// Package runtime defines the runtime-neutral durable execution contract.
//
// Flow code (the FlowHost in flow/host and the node handlers it dispatches to,
// ADR-0011) is written once against DurableContext and runs on every Runtime
// implementation: runtime/local (tests, CLI, fast interactive
// runs; not durable) and runtime/temporal (embedded SQLite server for the OSS
// edition, Temporal Cloud or a self-hosted cluster for SaaS). See ADR-0002 and
// docs/architecture/runtime.md.
//
// Status: contract implemented; runtime/local implemented (WP-L5-01);
// runtime/temporal implemented through the control plane (WP-L5-02 pull
// requests (a), (b), (c); embedded packaging WP-L5-03).
package runtime

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"time"

	"github.com/epzero/basamento/core"
)

// DurableContext is everything flow code may do. Every method is replay-safe:
// a runtime must return the same results when it replays a run from history.
type DurableContext interface {
	Context() context.Context
	Now() time.Time             // workflow time, never the wall clock
	NewID(kind string) string   // deterministic per run
	Rand(key string) *rand.Rand // seeded from (run, key)
	Step(name string, in any, o StepOptions) Future
	Child(flow FlowRef, in any, o ChildOptions) Future
	Sleep(d time.Duration) Future
	Await(g GateSpec) Future          // human or external event; validated payload
	Select(fs ...Future) (int, error) // deterministic wait-any; returns the ready index
	Emit(ev ProgressEvent) error      // durable outbox write (ordered per run)
	Checkpoint(s SnapshotRef) error   // ContinueAsNew where supported; no-op elsewhere
	Capabilities() Caps
}

// Future is a pending result of a durable operation.
type Future interface {
	Get(out any) error
	Ready() bool
}

// StepOptions configures one side-effecting step (a Temporal activity on the temporal runtime).
type StepOptions struct {
	Queue          string        // task queue / worker pool; empty = default
	StartToClose   time.Duration // per attempt
	Heartbeat      time.Duration // required for long or streaming steps; cancellation arrives via heartbeat
	MaxAttempts    int           // 0 = runtime default
	NonRetryable   []string      // core.Code values that stop retries
	IdempotencyKey string        // stable across retries of the same logical step
}

// ChildOptions configures a child flow.
type ChildOptions struct {
	ID          string // deterministic child run id
	ParentClose string // "terminate" | "abandon" | "request_cancel"
}

// GateSpec describes a wait for an external decision.
type GateSpec struct {
	Name     string
	Schema   json.RawMessage // JSON Schema of the accepted payload
	AcceptIf string          // CEL over `input` and `state`; empty = accept any valid payload
	Timeout  time.Duration   // 0 = no timeout
}

// FlowRef names a flow definition by content hash.
type FlowRef struct {
	Name string
	Hash string // sha256 of the canonical IR
}

// SnapshotRef points to a state snapshot stored in blob storage.
type SnapshotRef struct {
	BlobKey string
	Seq     int64
}

// ProgressEvent is a durable run event (see package stream for the wire shape).
type ProgressEvent struct {
	Type string
	Data json.RawMessage
}

// Caps advertises what a runtime supports so callers never fake semantics.
type Caps struct {
	Durable         bool // survives process crash
	SyncGateUpdates bool // gate payload validated synchronously (Temporal Update)
	ContinueAsNew   bool
	PolyglotQueues  bool // steps may run on non-Go workers
}

// StepFunc is a registered side-effecting step.
type StepFunc func(ctx context.Context, in json.RawMessage) (json.RawMessage, error)

// StepDefaults are registration-time defaults for a step.
type StepDefaults struct {
	Options StepOptions
}

// FlowHost is the single generic entrypoint that interprets a flow IR. Package
// flow/host provides it; the composition root registers it with each Runtime.
type FlowHost func(dc DurableContext, flow FlowRef, input json.RawMessage) (json.RawMessage, error)

// StartRequest starts a run.
//
// The tenant comes from ctx with core.TenantFrom, as for AuditEvent.Tenant in
// platform/platform.go: TenantID must be empty or equal to it, and a Runtime
// rejects a different one, or a call with no context tenant, with code
// invalid_argument rather than trust the request over the context
// (REQ-RT-040, REQ-RT-042, REQ-RT-044). The field never overrides the context.
type StartRequest struct {
	Flow           FlowRef
	Input          json.RawMessage
	RunID          string // optional; generated when empty
	IdempotencyKey string // a second Start with the same key returns the same run
	// TenantID must be empty or the context's tenant, never a different one.
	TenantID core.TenantID
}

// RunRef identifies a run, as Start returns it and as it is passed to
// Resolve, Cancel and Describe.
//
// The tenant comes from ctx with core.TenantFrom, as for AuditEvent.Tenant in
// platform/platform.go: TenantID must be empty or equal to it, and a Runtime
// rejects a different one, or a call with no context tenant, with code
// invalid_argument before the same-tenant lookup (REQ-RT-042, REQ-RT-044,
// REQ-RT-045). The field never overrides the context, so a ref naming another
// tenant's run cannot reach it. A Runtime sets TenantID on the refs it
// returns, in Start and in RunStatus.Ref (REQ-RT-043).
type RunRef struct {
	RunID string
	// TenantID must be empty or the context's tenant, never a different one.
	TenantID core.TenantID
}

// RunStatus is a run's observable state.
type RunStatus struct {
	Ref       RunRef
	State     string // running | completed | failed | cancelled | terminated
	StartedAt time.Time
	ClosedAt  *time.Time
}

// RunQuery filters List.
//
// The tenant comes from ctx with core.TenantFrom, as for AuditEvent.Tenant in
// platform/platform.go: TenantID must be empty or equal to it, and a Runtime
// rejects a different one, or a call with no context tenant, with code
// invalid_argument (REQ-RT-042, REQ-RT-044, REQ-RT-045). The field never
// overrides the context: List returns only the context tenant's runs
// (REQ-RT-026).
type RunQuery struct {
	// TenantID must be empty or the context's tenant, never a different one.
	TenantID core.TenantID
	Flow     string
	State    string
	Limit    int
	Cursor   string
}

// Runtime is the control plane of one durable execution engine.
type Runtime interface {
	RegisterFlowHost(h FlowHost) error
	RegisterStep(name string, fn StepFunc, d StepDefaults) error
	Start(ctx context.Context, r StartRequest) (RunRef, error)
	Resolve(ctx context.Context, run RunRef, gate string, payload json.RawMessage) error
	Cancel(ctx context.Context, run RunRef, reason string) error
	Describe(ctx context.Context, run RunRef) (RunStatus, error)
	List(ctx context.Context, q RunQuery) ([]RunStatus, string, error)
	Serve(ctx context.Context) error // workers (and the embedded server when the profile says so)
	Capabilities() Caps
}
