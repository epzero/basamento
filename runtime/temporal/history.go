package temporal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
	"github.com/epzero/basamento/runtime"
)

// History event types the runtime records. Temporal's own event vocabulary is
// larger; these are the events this runtime needs to observe and replay a run.
const (
	eventWorkflowStarted   = "workflow_started"
	eventWorkflowTask      = "workflow_task_started"
	eventActivityScheduled = "activity_scheduled"
	eventActivityStarted   = "activity_started"
	eventActivityCompleted = "activity_completed"
	eventActivityFailed    = "activity_failed"
	eventTimerStarted      = "timer_started"
	eventTimerFired        = "timer_fired"
	eventGateAwaited       = "gate_awaited"
	eventGateResolved      = "gate_resolved"
	eventSignal            = "signal_received"
	eventEmitted           = "event_emitted"
	eventContinuedAsNew    = "continued_as_new"
	eventCompleted         = "workflow_completed"
	eventFailed            = "workflow_failed"
	eventCancelled         = "workflow_cancelled"
)

// HistoryEvent is one event of a run history. Payload is the encoded payload
// the event carries; a payload is nil for events without one.
type HistoryEvent struct {
	Seq        int64
	Type       string
	Name       string
	Attempt    int
	At         time.Time
	Tenant     core.TenantID
	Payload    *secrets.Payload
	ClaimCheck bool
}

// jsonPayload wraps literal JSON as a plain payload. The codec encrypts it on
// the way into a history (REQ-RT-031).
func jsonPayload(raw json.RawMessage) secrets.Payload {
	return secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     append([]byte(nil), raw...),
	}
}

// record appends an event to the run history and counts it in the current
// continue-as-new segment.
func (r *run) record(eventType, name string, attempt int, encoded *Encoded) {
	r.mu.Lock()
	r.recordLocked(eventType, name, attempt, encoded)
	r.mu.Unlock()
	r.notify()
}

func (r *run) recordLocked(eventType, name string, attempt int, encoded *Encoded) {
	r.seq++
	event := HistoryEvent{
		Seq:     r.seq,
		Type:    eventType,
		Name:    name,
		Attempt: attempt,
		At:      r.rt.clock.Now(),
		Tenant:  r.tenant,
	}
	if encoded != nil {
		payload := encoded.Payload
		event.Payload = &payload
		event.ClaimCheck = encoded.ClaimCheck
	}
	r.history = append(r.history, event)
	r.segment++
}

// History returns a copy of a run's history. The tenant comes from ctx and
// must agree with ref.TenantID when set, as every run lookup does.
func (r *Runtime) History(ctx context.Context, ref runtime.RunRef) ([]HistoryEvent, error) {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return nil, err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return nil, notFound(ref.RunID)
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	return append([]HistoryEvent(nil), target.history...), nil
}

// ContinueAsNewCount returns how many times a run continued as new.
func (r *Runtime) ContinueAsNewCount(ctx context.Context, ref runtime.RunRef) (int, error) {
	events, err := r.History(ctx, ref)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, event := range events {
		if event.Type == eventContinuedAsNew {
			n++
		}
	}
	return n, nil
}
