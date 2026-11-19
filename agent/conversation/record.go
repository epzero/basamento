package conversation

import (
	"encoding/json"
	"fmt"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Mode is the policy a user message carries (ADR-0007, design §3).
type Mode string

// The message modes. An empty mode means ModeQueue.
const (
	ModeQueue  Mode = "queue"
	ModeSteer  Mode = "steer"
	ModeCancel Mode = "cancel"
)

// Turn states, as the coordinator records them.
const (
	TurnRunning   = "running"
	TurnCompleted = "completed"
	TurnFailed    = "failed"
	TurnCancelled = "cancelled"
)

// Message roles and statuses in the conversation history.
const (
	RoleUser  = "user"
	RoleModel = "model"

	StatusComplete    = "complete"
	StatusInterrupted = "interrupted"
)

// DefaultCheckpointThreshold is the number of the coordinator's own durable
// calls after which it checkpoints at a turn boundary (REQ-CONV-014, design §4).
const DefaultCheckpointThreshold = 1000

// Principal is who asks for a read, send or cancel. The tenant comes from the
// caller's context in production; the user from the authenticated principal.
type Principal struct {
	Tenant core.TenantID
	User   string
}

// Message is one user message. ID makes a redelivery a no-op (REQ-CONV-005).
type Message struct {
	ID     string        `json:"id"`
	Tenant core.TenantID `json:"tenant,omitempty"`
	User   string        `json:"user,omitempty"`
	Text   string        `json:"text"`
	Mode   Mode          `json:"mode,omitempty"`
}

// Turn is one turn's record: its state, failure code and child run id.
type Turn struct {
	Index  int    `json:"index"`
	State  string `json:"state"`
	Code   string `json:"code,omitempty"`
	Child  string `json:"child,omitempty"`
	UserID string `json:"user_id,omitempty"`
}

// MessageRecord is one message in the conversation history. A model message is
// identified by its turn and position (design §8); Position is zero for user
// messages.
type MessageRecord struct {
	Turn     int    `json:"turn"`
	Role     string `json:"role"`
	Text     string `json:"text"`
	ID       string `json:"id,omitempty"`
	Position int    `json:"position,omitempty"`
	Status   string `json:"status"`
}

// ModelMessage is one model message a turn produced. Status is complete or
// interrupted (REQ-CONV-009, REQ-CONV-013).
type ModelMessage struct {
	Text   string `json:"text"`
	Status string `json:"status,omitempty"`
}

// TurnInput is what the coordinator gives a turn's child run: the history of
// the conversation's completed, failed and cancelled turns, followed by the
// user message that starts this turn (REQ-CONV-012).
type TurnInput struct {
	Conversation string          `json:"conversation"`
	Turn         int             `json:"turn"`
	Messages     []MessageRecord `json:"messages"`
}

// TurnOutput is what a turn's child run reports back. Messages are its model
// messages in order; a partial message whose turn was cancelled or failed is
// interrupted.
type TurnOutput struct {
	State    string         `json:"state"`
	Code     string         `json:"code,omitempty"`
	Messages []ModelMessage `json:"messages,omitempty"`
}

// TurnStart is a turn the coordinator has begun: its index and the input for
// its child run.
type TurnStart struct {
	Index int
	Input TurnInput
}

// Record is the coordinator's durable state (design §2).
type Record struct {
	Tenant    core.TenantID   `json:"tenant"`
	User      string          `json:"user"`
	ID        string          `json:"id"`
	Flow      runtime.FlowRef `json:"flow"`
	Turns     []Turn          `json:"turns,omitempty"`
	Messages  []MessageRecord `json:"messages,omitempty"`
	Queue     []Message       `json:"queue,omitempty"`
	Handled   []string        `json:"handled,omitempty"`
	Running   int             `json:"running,omitempty"` // 1-based turn index
	Cancelled bool            `json:"cancelled,omitempty"`
	Calls     int             `json:"calls,omitempty"`     // since this run started
	Continued int             `json:"continued,omitempty"` // continue-as-new count
}

// NewRecord returns an empty conversation record.
func NewRecord(tenant core.TenantID, user, id string, flow runtime.FlowRef) Record {
	return Record{Tenant: tenant, User: user, ID: id, Flow: flow}
}

// RunID is the coordinator run id of a conversation (REQ-CONV-001).
func RunID(id string) string { return "conv-" + id }

// Authorize checks that p may address the conversation. Another tenant's
// conversation is not found; another user of the same tenant is denied
// (REQ-CONV-016 to REQ-CONV-018).
func (r *Record) Authorize(p Principal) error {
	if p.Tenant != r.Tenant {
		return core.NewError(core.CodeNotFound,
			fmt.Sprintf("conversation %q: not found", r.ID),
			map[string]any{"conversation": r.ID})
	}
	if p.User != r.User {
		return core.NewError(core.CodePermissionDenied,
			fmt.Sprintf("conversation %q belongs to another user", r.ID),
			map[string]any{"conversation": r.ID})
	}
	return nil
}

// DecisionKind is what the coordinator does with an accepted message.
type DecisionKind string

// The decisions Accept can return.
const (
	DecisionDuplicate  DecisionKind = "duplicate"
	DecisionStart      DecisionKind = "start"
	DecisionQueued     DecisionKind = "queued"
	DecisionSteer      DecisionKind = "steer"
	DecisionCancelTurn DecisionKind = "cancel_turn"
)

// Decision is what the coordinator does with a user message after validating
// and folding it into the record.
type Decision struct {
	Kind    DecisionKind
	Message Message
}

// Accept validates and folds msg into the record. It does not start or end a
// turn; the caller does that from the decision. A message id already accepted
// is a duplicate and runs no second turn (REQ-CONV-005). Steering and cancel
// with no running turn are refused (REQ-CONV-020, REQ-CONV-021).
func (r *Record) Accept(msg Message) (Decision, error) {
	if msg.ID == "" {
		return Decision{}, core.NewError(core.CodeInvalidArgument, "conversation: a message needs an id", nil)
	}
	if msg.Mode == "" {
		msg.Mode = ModeQueue
	}
	if msg.Mode != ModeQueue && msg.Mode != ModeSteer && msg.Mode != ModeCancel {
		return Decision{}, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("conversation: unknown message mode %q", msg.Mode),
			map[string]any{"mode": string(msg.Mode)})
	}
	if r.Cancelled {
		return Decision{}, core.NewError(core.CodeConflict,
			"conversation: it was cancelled",
			map[string]any{"conversation": r.ID})
	}
	for _, id := range r.Handled {
		if id == msg.ID {
			return Decision{Kind: DecisionDuplicate, Message: msg}, nil
		}
	}
	if msg.Mode == ModeSteer && r.Running == 0 {
		return Decision{}, core.NewError(core.CodeConflict,
			"conversation: no turn is running to steer",
			map[string]any{"conversation": r.ID})
	}
	if msg.Mode == ModeCancel && r.Running == 0 {
		return Decision{}, core.NewError(core.CodeConflict,
			"conversation: no turn is running to cancel",
			map[string]any{"conversation": r.ID})
	}
	r.Handled = append(r.Handled, msg.ID)
	switch msg.Mode {
	case ModeSteer:
		return Decision{Kind: DecisionSteer, Message: msg}, nil
	case ModeCancel:
		return Decision{Kind: DecisionCancelTurn, Message: msg}, nil
	default:
		r.Queue = append(r.Queue, msg)
		if r.Running == 0 {
			return Decision{Kind: DecisionStart, Message: msg}, nil
		}
		return Decision{Kind: DecisionQueued, Message: msg}, nil
	}
}

// BeginTurn starts the first queued message's turn: it records the turn and
// its user message and returns the history its child run sees. The caller must
// ensure a turn is not already running.
func (r *Record) BeginTurn() (TurnStart, bool) {
	if r.Running != 0 || len(r.Queue) == 0 {
		return TurnStart{}, false
	}
	msg := r.Queue[0]
	r.Queue = r.Queue[1:]
	turn := Turn{Index: len(r.Turns) + 1, State: TurnRunning, UserID: msg.ID}
	r.Turns = append(r.Turns, turn)
	r.Running = turn.Index
	r.Messages = append(r.Messages, MessageRecord{
		Turn:   turn.Index,
		Role:   RoleUser,
		Text:   msg.Text,
		ID:     msg.ID,
		Status: StatusComplete,
	})
	return TurnStart{Index: turn.Index, Input: TurnInput{Conversation: r.ID, Turn: turn.Index, Messages: r.History()}}, true
}

// Steer appends a steering message to the running turn's history
// (REQ-CONV-007). The caller delivers it to the child run's input channel.
func (r *Record) Steer(id, text string) bool {
	if r.Running == 0 {
		return false
	}
	r.Messages = append(r.Messages, MessageRecord{
		Turn:   r.Running,
		Role:   RoleUser,
		Text:   text,
		ID:     id,
		Status: StatusComplete,
	})
	return true
}

// ModelMessage appends one complete model message to the running turn at its
// next position. It is the step boundary between two model calls of a turn.
func (r *Record) ModelMessage(m ModelMessage) {
	if r.Running == 0 {
		return
	}
	position := 0
	for _, msg := range r.Messages {
		if msg.Turn == r.Running && msg.Role == RoleModel && msg.Position > position {
			position = msg.Position
		}
	}
	status := m.Status
	if status == "" {
		status = StatusComplete
	}
	r.Messages = append(r.Messages, MessageRecord{
		Turn:     r.Running,
		Role:     RoleModel,
		Text:     m.Text,
		Position: position + 1,
		Status:   status,
	})
}

// RecordModelMessage records one model message of turn at position in the
// conversation history. It is the turn's recording step: called again for the
// same turn and position, it appends nothing, completes an interrupted message
// in place, and reports false (REQ-CONV-023, design §8).
func (r *Record) RecordModelMessage(turn, position int, m ModelMessage) bool {
	if turn <= 0 || position <= 0 {
		return false
	}
	status := m.Status
	if status == "" {
		status = StatusComplete
	}
	for i := range r.Messages {
		if r.Messages[i].Turn == turn && r.Messages[i].Role == RoleModel && r.Messages[i].Position == position {
			if r.Messages[i].Status == StatusInterrupted && status == StatusComplete {
				r.Messages[i].Status = StatusComplete
			}
			if r.Messages[i].Text == "" {
				r.Messages[i].Text = m.Text
			}
			return false
		}
	}
	r.Messages = append(r.Messages, MessageRecord{
		Turn:     turn,
		Role:     RoleModel,
		Text:     m.Text,
		Position: position,
		Status:   status,
	})
	return true
}

// EndTurn records the running turn's result and its model messages. A model
// message already recorded for the same turn and position is completed in
// place instead of appended again, so a replayed recording step records it at
// most once (REQ-CONV-023, design §8).
func (r *Record) EndTurn(result TurnOutput) {
	if r.Running == 0 {
		return
	}
	state := result.State
	switch state {
	case TurnCompleted, TurnFailed, TurnCancelled:
	default:
		state = TurnFailed
	}
	turn := &r.Turns[r.Running-1]
	turn.State = state
	turn.Code = result.Code
	for position, m := range result.Messages {
		r.RecordModelMessage(turn.Index, position+1, m)
	}
	r.Running = 0
}

// CancelConversation drops the queue and marks the conversation cancelled
// (REQ-CONV-011). The caller cancels the running child run.
func (r *Record) CancelConversation() {
	r.Cancelled = true
	r.Queue = nil
	if r.Running > 0 {
		turn := &r.Turns[r.Running-1]
		turn.State = TurnCancelled
		turn.Code = string(core.CodeCancelled)
	}
	r.Running = 0
}

// History returns the messages of completed, failed and cancelled turns in
// order (REQ-CONV-012). A running turn's partial output is not history for a
// new turn; the coordinator never starts one while a turn runs.
func (r *Record) History() []MessageRecord {
	out := make([]MessageRecord, 0, len(r.Messages))
	return append(out, r.Messages...)
}

// LastTurn returns the most recently ended turn.
func (r *Record) LastTurn() (Turn, bool) {
	if len(r.Turns) == 0 {
		return Turn{}, false
	}
	return r.Turns[len(r.Turns)-1], true
}

// DurableCall counts one durable call the coordinator itself made: an await,
// selection, step, child, sleep or emit. Child-run calls are not counted
// (REQ-CONV-014, design §4).
func (r *Record) DurableCall() { r.Calls++ }

// CheckpointDue reports whether the coordinator reaches a turn boundary with
// at least threshold of its own durable calls since it last started. The
// runtime must support continuing as new (REQ-CONV-014).
func (r *Record) CheckpointDue(threshold int, caps runtime.Caps) bool {
	if !caps.ContinueAsNew {
		return false
	}
	if threshold <= 0 {
		threshold = DefaultCheckpointThreshold
	}
	return r.Calls >= threshold
}

// Checkpointed resets the durable-call count and counts a continuation
// (REQ-CONV-014).
func (r *Record) Checkpointed() {
	r.Calls = 0
	r.Continued++
}

// Marshal encodes the record as JSON for the run's state.
func (r Record) Marshal() json.RawMessage {
	raw, err := json.Marshal(r)
	if err != nil {
		panic("conversation: marshal record: " + err.Error())
	}
	return raw
}
