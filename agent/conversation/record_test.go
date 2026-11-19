package conversation

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func newTestRecord() Record {
	return NewRecord("acme", "ana", "c1", runtime.FlowRef{Name: "assistant", Hash: "h"})
}

func TestAuthorize(t *testing.T) {
	rec := newTestRecord()
	if err := rec.Authorize(Principal{Tenant: "acme", User: "ana"}); err != nil {
		t.Fatalf("same tenant and user: %v", err)
	}
	if err := rec.Authorize(Principal{Tenant: "globex", User: "ana"}); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("another tenant: %v, want not_found", err)
	}
	if err := rec.Authorize(Principal{Tenant: "acme", User: "ben"}); core.CodeOf(err) != core.CodePermissionDenied {
		t.Fatalf("another user: %v, want permission_denied", err)
	}
}

func TestAcceptDeduplicatesByID(t *testing.T) {
	rec := newTestRecord()
	first, err := rec.Accept(Message{ID: "m1", Text: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != DecisionStart {
		t.Fatalf("kind = %s, want start", first.Kind)
	}
	if _, ok := rec.BeginTurn(); !ok {
		t.Fatal("no turn started")
	}
	again, err := rec.Accept(Message{ID: "m1", Text: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Kind != DecisionDuplicate {
		t.Fatalf("kind = %s, want duplicate", again.Kind)
	}
	sameText, err := rec.Accept(Message{ID: "m2", Text: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if sameText.Kind != DecisionQueued {
		t.Fatalf("same text, new id: %s, want queued while a turn runs", sameText.Kind)
	}
	if got := len(rec.Handled); got != 2 {
		t.Fatalf("handled = %d, want 2", got)
	}
}

func TestAcceptRefusesSteerAndCancelWithoutTurn(t *testing.T) {
	rec := newTestRecord()
	if _, err := rec.Accept(Message{ID: "m1", Text: "later", Mode: ModeSteer}); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("steer: %v, want conflict", err)
	}
	if _, err := rec.Accept(Message{ID: "m2", Text: "stop", Mode: ModeCancel}); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("cancel: %v, want conflict", err)
	}
}

func TestBeginTurnHistoryAndQueueOrder(t *testing.T) {
	rec := newTestRecord()
	for _, m := range []Message{
		{ID: "m1", Text: "One"},
		{ID: "m2", Text: "Two"},
	} {
		if _, err := rec.Accept(m); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.Queue) != 2 {
		t.Fatalf("queue = %d, want 2", len(rec.Queue))
	}
	start, ok := rec.BeginTurn()
	if !ok {
		t.Fatal("BeginTurn returned no turn")
	}
	if start.Index != 1 {
		t.Fatalf("turn = %d, want 1", start.Index)
	}
	if len(start.Input.Messages) != 1 || start.Input.Messages[0].Text != "One" {
		t.Fatalf("history = %+v, want the starting user message", start.Input.Messages)
	}
	if len(rec.Queue) != 1 || rec.Queue[0].Text != "Two" {
		t.Fatalf("queue = %+v, want Two", rec.Queue)
	}
	rec.EndTurn(TurnOutput{State: TurnCompleted, Messages: []ModelMessage{{Text: "First"}}})
	second, ok := rec.BeginTurn()
	if !ok {
		t.Fatal("second BeginTurn returned no turn")
	}
	want := []string{"One", "First", "Two"}
	if len(second.Input.Messages) != len(want) {
		t.Fatalf("history = %+v, want %v", second.Input.Messages, want)
	}
	for i, w := range want {
		if second.Input.Messages[i].Text != w {
			t.Fatalf("history[%d] = %q, want %q", i, second.Input.Messages[i].Text, w)
		}
	}
}

func TestSteerAppendsToRunningTurn(t *testing.T) {
	rec := newTestRecord()
	if _, err := rec.Accept(Message{ID: "m1", Text: "Write"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.BeginTurn(); !ok {
		t.Fatal("no turn")
	}
	if !rec.Steer("m2", "About the sea") {
		t.Fatal("Steer refused a running turn")
	}
	if got := rec.Messages[len(rec.Messages)-1]; got.Text != "About the sea" || got.Role != RoleUser {
		t.Fatalf("last message = %+v, want the steering user message", got)
	}
}

func TestEndTurnRecordsInterruptedAndCompletesInPlace(t *testing.T) {
	rec := newTestRecord()
	if _, err := rec.Accept(Message{ID: "m1", Text: "Look up"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.BeginTurn(); !ok {
		t.Fatal("no turn")
	}
	rec.EndTurn(TurnOutput{
		State:    TurnFailed,
		Code:     "vendor_error",
		Messages: []ModelMessage{{Text: "Searching", Status: StatusInterrupted}},
	})
	turn, _ := rec.LastTurn()
	if turn.State != TurnFailed || turn.Code != "vendor_error" {
		t.Fatalf("turn = %+v, want failed vendor_error", turn)
	}
	// The recording step runs again with the completed stream (REQ-CONV-023).
	rec.Running = 1
	rec.EndTurn(TurnOutput{State: TurnFailed, Messages: []ModelMessage{{Text: "Searching"}}})
	models := 0
	for _, m := range rec.Messages {
		if m.Role == RoleModel {
			models++
			if m.Text != "Searching" || m.Status != StatusComplete {
				t.Fatalf("model message = %+v, want the interrupted one completed in place", m)
			}
		}
	}
	if models != 1 {
		t.Fatalf("model messages = %d, want 1", models)
	}
}

func TestRecordModelMessageIsIdempotent(t *testing.T) {
	rec := newTestRecord()
	if _, err := rec.Accept(Message{ID: "m1", Text: "Tell me a story"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.BeginTurn(); !ok {
		t.Fatal("no turn")
	}
	if !rec.RecordModelMessage(1, 1, ModelMessage{Text: "Once upon a time"}) {
		t.Fatal("the first recording was not recorded")
	}
	if rec.RecordModelMessage(1, 1, ModelMessage{Text: "Once upon a time"}) {
		t.Fatal("the replayed recording appended a second message")
	}
	models := 0
	for _, m := range rec.Messages {
		if m.Role == RoleModel {
			models++
		}
	}
	if models != 1 {
		t.Fatalf("model messages = %d, want 1", models)
	}
}

// TestRecordWithQueueSurvivesMarshalling is the coordinator's ContinueAsNew and
// crash-resume contract: the record that carries the running turn, the queue,
// the handled ids and the continuation count survives an encode and decode.
func TestRecordWithQueueSurvivesMarshalling(t *testing.T) {
	rec := newTestRecord()
	if _, err := rec.Accept(Message{ID: "m1", Text: "Write a poem"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.BeginTurn(); !ok {
		t.Fatal("no turn")
	}
	for _, msg := range []Message{{ID: "m2", Text: "Then translate it"}, {ID: "m3", Text: "And add a title"}} {
		if _, err := rec.Accept(msg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rec.Accept(Message{ID: "m4", Text: "Make it rhyme", Mode: ModeSteer}); err != nil {
		t.Fatal(err)
	}
	rec.Steer("m4", "Make it rhyme")
	rec.DurableCall()
	rec.Checkpointed()

	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var resumed Record
	if err := json.Unmarshal(raw, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Running != 1 || len(resumed.Queue) != 2 || len(resumed.Handled) != 4 || resumed.Continued != 1 {
		t.Fatalf("resumed record = %+v, want the running turn, two queued messages, four handled ids and one continuation", resumed)
	}
	if len(resumed.Messages) != 2 || resumed.Messages[1].Text != "Make it rhyme" || resumed.Messages[1].Role != RoleUser {
		t.Fatalf("messages = %+v, want the steering message kept", resumed.Messages)
	}
	if _, err := resumed.Accept(Message{ID: "m2", Text: "Then translate it"}); err != nil {
		t.Fatalf("redelivered queued message: %v", err)
	}
	if len(resumed.Queue) != 2 {
		t.Fatalf("queue = %d, want the duplicate to be a no-op", len(resumed.Queue))
	}
}

func TestCancelConversationDropsQueue(t *testing.T) {
	rec := newTestRecord()
	if _, err := rec.Accept(Message{ID: "m1", Text: "Write"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.BeginTurn(); !ok {
		t.Fatal("no turn")
	}
	if _, err := rec.Accept(Message{ID: "m2", Text: "Then"}); err != nil {
		t.Fatal(err)
	}
	rec.CancelConversation()
	if !rec.Cancelled {
		t.Fatal("conversation not cancelled")
	}
	if len(rec.Queue) != 0 {
		t.Fatalf("queue = %+v, want empty", rec.Queue)
	}
	if _, err := rec.Accept(Message{ID: "m3", Text: "Still there?"}); err == nil {
		t.Fatal("cancelled conversation accepted a message")
	}
}

func TestCheckpointDue(t *testing.T) {
	rec := newTestRecord()
	for i := 0; i < 7; i++ {
		rec.DurableCall()
	}
	if rec.CheckpointDue(7, runtime.Caps{ContinueAsNew: false}) {
		t.Fatal("checkpointed without continue-as-new")
	}
	if !rec.CheckpointDue(7, runtime.Caps{ContinueAsNew: true}) {
		t.Fatal("no checkpoint at the threshold")
	}
	rec.Checkpointed()
	if rec.Calls != 0 || rec.Continued != 1 {
		t.Fatalf("after checkpoint: calls=%d continued=%d", rec.Calls, rec.Continued)
	}
	if rec.CheckpointDue(0, runtime.Caps{ContinueAsNew: true}) {
		t.Fatal("checkpointed below the default threshold")
	}
}
