// Package conversation implements a conversation as one coordinator run that
// starts exactly one child run per turn (REQ-CONV-001, REQ-CONV-002; ADR-0007).
//
// The coordinator is written against runtime.DurableContext and imports no
// durable-execution vendor (ADR-0002). A user message reaches it as a gate
// resolution; while a turn runs the coordinator races the next message against
// the turn's child run and applies the message's policy: queue, steer or
// cancel. Its durable record holds the turns, the messages, the queue and the
// ids it has already accepted, so a restart or a continuation keeps them
// (design §2).
//
// Status: implemented (WP-L7-03). The turn workflow itself is WP-L7-01; a
// conversation on the temporal runtime is WP-L7-04.
package conversation
