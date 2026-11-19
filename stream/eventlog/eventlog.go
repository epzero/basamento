package eventlog

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/epzero/basamento/core"
	psql "github.com/epzero/basamento/platform/sql"
)

// DefaultSchemaVersion is the schema version stamped on an event that does not
// name one. Bump it only with a contract change; consumers read the version
// from the envelope.
const DefaultSchemaVersion = 1

// Fault is the deterministic seam tests use to stop the writer or the relay at
// a named point of its path, as specs/streaming/design.md requires. A point
// names the step that was reached; returning an error aborts the step (the
// write transaction rolls back, or the relay pass ends) and a nil result
// continues. Production passes nil, so no point can fire.
type Fault func(point string) error

// Options configures a Log.
type Options struct {
	// Clock is the only source of the created and relayed timestamps. A nil
	// Clock uses the system clock.
	Clock core.Clock
	// Fault is the test-only seam described on Fault; nil in production.
	Fault Fault
}

// Log is the durable event log of every run in one SQL store.
type Log struct {
	store *psql.Store
	clock core.Clock
	fault Fault
}

// New wraps store. The store must carry the migrations returned by Migrations.
func New(store *psql.Store, opts Options) (*Log, error) {
	if store == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "eventlog: nil store", nil)
	}
	clock := opts.Clock
	if clock == nil {
		clock = core.SystemClock{}
	}
	return &Log{store: store, clock: clock, fault: opts.Fault}, nil
}

// NewEvent is a durable event a state change records, before the log assigns
// its sequence number.
type NewEvent struct {
	// Type is the event type, such as "node.started"; the bus subject carries
	// the run only and the type travels in the envelope (REQ-STRM-024).
	Type string
	// V is the event schema version; zero means DefaultSchemaVersion.
	V int
	// Data is the event payload as JSON. Nil is stored and published as {}.
	Data json.RawMessage
}

// Event is a durable event of a run as recorded in the log.
type Event struct {
	RunID     string
	Seq       uint64
	Type      string
	V         int
	Data      json.RawMessage
	CreatedAt time.Time
}

// OutboxEntry is one outbox row of a run.
type OutboxEntry struct {
	Seq       uint64
	Relayed   bool
	RelayedAt time.Time
}

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ValidRunID reports whether runID can be a bus subject token and therefore a
// log key. Run ids are letters, digits, '-' and '_', at most 128 bytes.
func ValidRunID(runID string) bool { return runIDPattern.MatchString(runID) }

func requireTenant(ctx context.Context) (core.TenantID, error) {
	id, ok := core.TenantFrom(ctx)
	if !ok {
		return "", core.NewError(core.CodeInvalidArgument, "eventlog: tenant required", nil)
	}
	return id, nil
}

func checkRunID(runID string) error {
	if !ValidRunID(runID) {
		return core.NewError(core.CodeInvalidArgument, "eventlog: invalid run id", map[string]any{"run_id": runID})
	}
	return nil
}

func checkState(state string) error {
	if state == "" || len(state) > 128 {
		return core.NewError(core.CodeInvalidArgument, "eventlog: invalid run state", nil)
	}
	return psql.ValidateText(state)
}

func normalizeEvent(e NewEvent) (typeName string, version int, data json.RawMessage, err error) {
	if e.Type == "" || len(e.Type) > 256 {
		return "", 0, nil, core.NewError(core.CodeInvalidArgument, "eventlog: invalid event type", nil)
	}
	if err := psql.ValidateText(e.Type); err != nil {
		return "", 0, nil, err
	}
	version = e.V
	if version == 0 {
		version = DefaultSchemaVersion
	}
	if version < 0 || version > 1<<31-1 {
		return "", 0, nil, core.NewError(core.CodeInvalidArgument, "eventlog: invalid schema version", nil)
	}
	data = e.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	if !json.Valid(data) {
		return "", 0, nil, core.NewError(core.CodeInvalidArgument, "eventlog: event data is not JSON", nil)
	}
	return e.Type, version, data, nil
}

func (l *Log) fire(point string) error {
	if l.fault == nil {
		return nil
	}
	return l.fault(point)
}

// RunSubject returns the one bus subject of a run's durable events.
func RunSubject(runID string) (string, error) {
	if err := checkRunID(runID); err != nil {
		return "", err
	}
	return "run." + runID + ".events", nil
}
