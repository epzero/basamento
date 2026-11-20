// Package flow defines flows as data (IR), a compiler that type-checks CEL
// against the state schema, and a pure interpreter state machine.
//
// The Machine performs no I/O and reads no clock: the host in flow/host feeds
// it events and turns the commands it returns into DurableContext calls, so
// this package never imports package runtime (ADR-0011). This is what lets the same flow run on
// every runtime with identical results (REQ-FLOW-*). See ADR-0003.
//
// Status: IR, compiler and Go builder implemented (WP-L4-01, WP-L4-03);
// Machine contract only (WP-L4-02).
package flow

import "encoding/json"

// IR is a compiled flow definition. Its canonical JSON is content-addressed;
// a run pins the hash, so editing a flow never changes a running run.
type IR struct {
	Name     string                     `json:"name"`
	Version  int                        `json:"version"`
	State    StateSpec                  `json:"state"`
	Nodes    map[string]Node            `json:"nodes"`
	Edges    []Edge                     `json:"edges"`
	Defaults map[string]json.RawMessage `json:"defaults,omitempty"`
}

// StateSpec declares the state schema and per-key reducers.
type StateSpec struct {
	Schema   json.RawMessage   `json:"schema"`             // JSON Schema 2020-12
	Reducers map[string]string `json:"reducers,omitempty"` // key -> replace|append|merge|sum|merge_by(<field>)
}

// Node is one step of a flow.
type Node struct {
	Kind  string          `json:"kind"`          // set|task|model|agent|tool|map|parallel|subflow|gate|wait
	In    string          `json:"in,omitempty"`  // CEL or jq mapping from state to node input
	Out   string          `json:"out,omitempty"` // state key receiving the node's update
	Spec  json.RawMessage `json:"spec,omitempty"`
	Guard *Guard          `json:"guard,omitempty"`
}

// Guard bounds cycles and spend. A cycle needs a node on it whose guard carries
// a positive MaxVisits or CostMpts; a While condition alone does not bound it.
type Guard struct {
	MaxVisits int    `json:"max_visits,omitempty"`
	CostMpts  int64  `json:"cost_mpts,omitempty"`
	While     string `json:"while,omitempty"` // CEL
}

// Edge connects nodes; When is CEL over state. An edge without When is the default.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	When string `json:"when,omitempty"`
}

// Event is fed to the Machine by a runtime.
type Event struct {
	Type    string          `json:"type"` // node_completed|node_failed|gate_resolved|timer_fired|cancelled
	Node    string          `json:"node,omitempty"`
	Attempt int             `json:"attempt,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Command is returned by the Machine for the runtime to execute.
type Command struct {
	Type  string          `json:"type"` // run_node|start_timer|await_gate|emit|complete|fail|checkpoint
	Node  string          `json:"node,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Snapshot is the Machine's serializable state.
type Snapshot struct {
	Seq   int64           `json:"seq"`
	State json.RawMessage `json:"state"`
	Ready []string        `json:"ready,omitempty"`
	Visit map[string]int  `json:"visit,omitempty"`
	Cost  int64           `json:"cost_mpts"`
}

// Machine is the pure interpreter. Implementations must be deterministic:
// the same IR, input and event sequence always yield the same snapshots and
// commands, independent of the order in which parallel nodes completed
// (updates merge in canonical node-id order at superstep barriers).
type Machine interface {
	Start(ir *IR, input json.RawMessage) (Snapshot, []Command, error)
	Apply(s Snapshot, ev Event) (Snapshot, []Command, error)
}
