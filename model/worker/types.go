package worker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// Codes this layer adds to core's taxonomy. They are never renamed.
const (
	// CodeWorkerLost fails an invocation whose non-restart-safe attempt was
	// started on a worker that did not come back within the grace period
	// (REQ-MODEL-022, REQ-MODEL-025).
	CodeWorkerLost core.Code = "worker_lost"
	// CodeLeaseLost refuses a report on an invocation that has already ended
	// or whose lease the reporter no longer holds (REQ-MODEL-024).
	CodeLeaseLost core.Code = "lease_lost"
)

// Class is the priority class of an invocation. Interactive work goes first,
// then normal, then batch; ageing moves an invocation up one class after the
// pool's ageing period (REQ-MODEL-015, REQ-MODEL-016).
type Class string

const (
	ClassInteractive Class = "interactive"
	ClassNormal      Class = "normal"
	ClassBatch       Class = "batch"
)

// rank orders the classes, lower first.
func (c Class) rank() int {
	switch c {
	case ClassInteractive:
		return 0
	case ClassBatch:
		return 2
	default:
		return 1
	}
}

// ParseClass accepts the three classes; empty means normal.
func ParseClass(s string) (Class, error) {
	switch Class(strings.TrimSpace(s)) {
	case "":
		return ClassNormal, nil
	case ClassInteractive:
		return ClassInteractive, nil
	case ClassNormal:
		return ClassNormal, nil
	case ClassBatch:
		return ClassBatch, nil
	default:
		return "", core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("unknown class %q", s), map[string]any{"class": s})
	}
}

// State is what a worker is doing.
type State string

const (
	// StateActive takes work.
	StateActive State = "active"
	// StateDraining finishes what it holds, takes no more, then leaves.
	StateDraining State = "draining"
	// StatePaused takes no work but stays.
	StatePaused State = "paused"
	// StateLeft is a clean exit.
	StateLeft State = "left"
)

// Availability is what the catalog shows for a model a pool keeps.
type Availability string

const (
	AvailabilityOnline   Availability = "online"
	AvailabilityOffline  Availability = "offline"
	AvailabilityOnDemand Availability = "on_demand"
)

// Descriptor is what a worker declares for one model: the catalog descriptor
// plus the units it costs and whether an interrupted invocation may be
// started again (ADR-0012 §2, §7).
type Descriptor struct {
	model.Descriptor
	// Units the model costs on a worker; 0 reads as 1.
	Units int `json:"units,omitempty"`
	// RestartSafe says an interrupted invocation may be started again. False
	// is the default and always the case for tools and paid vendor calls.
	RestartSafe bool `json:"restart_safe,omitempty"`
}

// canonical returns a stable representation for descriptor comparison. A pool
// refuses a worker that declares a different descriptor for a model another
// present worker serves (REQ-MODEL-028).
func (d Descriptor) canonical() string {
	cp := d
	if cp.Units == 0 {
		cp.Units = 1
	}
	b, _ := json.Marshal(cp)
	return string(b)
}

// newToken mints an opaque token. Pool and join tokens are only ever stored
// hashed.
func newToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", core.NewError(core.CodeInternal, "worker: no randomness", nil)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// hashToken is the storage form of a pool token or credential.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// PoolSpec creates one pool. An empty Tenant makes the platform's shared
// pool; a set Tenant makes that tenant's own pool (ADR-0012 §1).
type PoolSpec struct {
	ID     string
	Tenant core.TenantID
	// Ageing is how long an invocation waits before it moves up one class.
	Ageing time.Duration
	// Lease is how long a lease lasts without a heartbeat.
	Lease time.Duration
	// Grace is how long a started, non-restart-safe invocation stays in doubt
	// for its own worker before it fails with worker_lost.
	Grace time.Duration
}

func (p PoolSpec) validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return core.NewError(core.CodeInvalidArgument, "worker: pool has no id", nil)
	}
	if p.Lease <= 0 {
		return core.NewError(core.CodeInvalidArgument, "worker: pool lease must be positive", nil)
	}
	if p.Grace <= 0 {
		return core.NewError(core.CodeInvalidArgument, "worker: pool grace must be positive", nil)
	}
	return nil
}

// Pool is a stored pool and the plaintext token returned once when it is
// created.
type Pool struct {
	PoolSpec
	Token string
}

// TenantSettings are a tenant's fairness and spill-over settings in a pool.
type TenantSettings struct {
	// Weight is the tenant's share of the shared pool; 0 reads as 1.
	Weight int
	// Concurrency is the most invocations the tenant may run at once in the
	// shared pool; 0 means no limit.
	Concurrency int
	// SpillOver lets queued work move to another provider of the model.
	SpillOver bool
	// SpillAfter is how long work waits before it may spill.
	SpillAfter time.Duration
}

// JoinRequest is one worker joining one pool.
type JoinRequest struct {
	PoolToken       string
	ProtocolVersion string
	Descriptors     []Descriptor
	Labels          map[string]string
	Capacity        int
}

// DefaultHeartbeatInterval is how often a worker heartbeats unless its join
// answer says otherwise (ADR-0012 §4).
const DefaultHeartbeatInterval = 30 * time.Second

// Worker is the platform's record of one worker.
type Worker struct {
	ID          string
	Pool        string
	State       State
	Descriptors []Descriptor
	Labels      map[string]string
	Capacity    int
	UnitsInUse  int
	Present     bool
	Removed     bool
	// Credential is the short-lived credential a successful join yields.
	Credential string
	// HeartbeatInterval is the cadence the join answer told the worker to
	// use; zero reads as DefaultHeartbeatInterval.
	HeartbeatInterval time.Duration
	// LastSeen is the last join, lease or heartbeat. A worker that misses
	// presenceMisses intervals is no longer present.
	LastSeen time.Time
}

// credentialLifetime is how many heartbeat intervals a join credential lasts
// without renewal.
const credentialLifetime = 120

// Event limits bound what one worker can write (ADR-0012 §10): a batch, one
// event and all events of an invocation.
const (
	MaxEventsPerBatch      = 256
	MaxEventBytes          = 256 << 10
	MaxEventBatchBytes     = 1 << 20
	MaxEventsPerInvocation = 10_000
)

// Invocation is one queued unit of work.
type Invocation struct {
	ClientRef    string
	Pool         string
	Tenant       core.TenantID
	Model        string
	Class        Class
	Units        int
	Request      json.RawMessage
	QueueTimeout time.Duration
	MaxRuntime   time.Duration
	ReservedMpts int64
	TraceParent  string

	State           string
	WorkerID        string
	Attempt         int
	Started         bool
	InDoubt         bool
	CancelRequested bool
	QueuedAt        time.Time
	// LeasedAt is when the current lease began; heartbeats do not move it.
	// It is what the lease-age metric measures (ADR-0012 §11).
	LeasedAt time.Time
	// NotBefore delays the next lease after a release (a vendor rate limit).
	NotBefore      time.Time
	LeaseExpires   time.Time
	InDoubtUntil   time.Time
	ChargedMpts    int64
	Code           core.Code
	Result         json.RawMessage
	ProviderHandle string
}

// LeaseRequest says what a worker can take right now.
type LeaseRequest struct {
	WorkerID   string
	Credential string
	FreeUnits  int
	WarmModels []string
	TimeLeft   time.Duration
	Wait       time.Duration
}

// HeartbeatAnswer is what the platform wants from a worker.
type HeartbeatAnswer struct {
	Cancel       []string
	Drain        bool
	LeaseExpires time.Time
	State        State
	// Credential is the rotated credential; empty means the caller keeps the
	// one it has.
	Credential string
	// CancelAttempts names the attempt each cancel refers to.
	CancelAttempts map[string]int
}

// QueueMetrics is what operating a pool reads (ADR-0012 §11).
type QueueMetrics struct {
	Pool        string
	Waiting     int
	Leased      int
	OldestWait  time.Duration
	MaxLeaseAge time.Duration
	UnitsInUse  int
	ByClass     map[Class]int
	ByModel     map[string]int
	Workers     map[State]int
	QueueDepth  map[string]int
}

// ErrLeaseLost reports a report on an invocation that already ended.
func errLeaseLost(clientRef string) error {
	return core.NewError(CodeLeaseLost,
		fmt.Sprintf("invocation %q is no longer leased", clientRef),
		map[string]any{"client_ref": clientRef})
}
