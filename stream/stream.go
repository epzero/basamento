package stream

import "encoding/json"

// Cursor is what a reconnecting client sends.
type Cursor struct {
	Seq  int64            `json:"seq"`  // last durable event seen
	Open map[string]int64 `json:"open"` // message id -> last delta seq seen
}

// DurableEvent is persisted and ordered per run by Seq.
type DurableEvent struct {
	RunID string          `json:"run_id"`
	Seq   int64           `json:"seq"`
	Type  string          `json:"type"` // run.*, node.*, message.started, message.completed, tool.*, gate.*
	V     int             `json:"v"`
	Data  json.RawMessage `json:"data"`
}

// Delta is ephemeral and ordered per message by DSeq.
type Delta struct {
	RunID     string  `json:"run_id"`
	MessageID string  `json:"message_id"`
	DSeq      int64   `json:"dseq"`
	Kind      string  `json:"kind"` // text|audio|progress|reset
	Text      string  `json:"text,omitempty"`
	Audio     []byte  `json:"audio,omitempty"`
	Progress  float64 `json:"progress,omitempty"`
	Attempt   int     `json:"attempt,omitempty"` // set on reset
}

// Snapshot is sent on reconnect for each in-flight message.
type Snapshot struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
	DSeq      int64  `json:"dseq"`
}
