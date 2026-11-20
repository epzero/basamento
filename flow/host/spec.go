// Node specification decoding: JSON node specs become step names, durations,
// gate settings and child flow references for the host's durable calls.
package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/runtime"
)

// jsonArgument passes an empty input as nil so a runtime stores no payload for
// a node that has none.
func jsonArgument(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return raw
}

func lookupNode(def *flow.IR, id string) (flow.Node, error) {
	node, ok := def.Nodes[id]
	if !ok {
		return flow.Node{}, invalid(fmt.Sprintf("flow host: node %q does not exist", id), map[string]any{"node": id})
	}
	return node, nil
}

func nameOf(node flow.Node, id string) (string, error) {
	var spec map[string]json.RawMessage
	if len(bytes.TrimSpace(node.Spec)) > 0 {
		if err := json.Unmarshal(node.Spec, &spec); err != nil {
			return "", invalid(fmt.Sprintf("flow: node %q: invalid %s spec: want a JSON object with a string %q", id, node.Kind, node.Kind), map[string]any{"node": id})
		}
	}
	name, ok := stringAt(spec, node.Kind)
	if !ok || name == "" {
		return "", invalid(fmt.Sprintf("flow: node %q: %s spec has no %q name", id, node.Kind, node.Kind), map[string]any{"node": id})
	}
	return name, nil
}

func durationOf(node flow.Node, id string) (time.Duration, error) {
	var spec map[string]json.RawMessage
	if len(bytes.TrimSpace(node.Spec)) > 0 {
		if err := json.Unmarshal(node.Spec, &spec); err != nil {
			return 0, invalid(fmt.Sprintf("flow: node %q: invalid wait spec: want a JSON object", id), map[string]any{"node": id})
		}
	}
	text, ok := stringAt(spec, "duration")
	if !ok || text == "" {
		return 0, invalid(fmt.Sprintf("flow: node %q: wait spec has no duration", id), map[string]any{"node": id})
	}
	duration, err := time.ParseDuration(text)
	if err != nil {
		return 0, invalid(fmt.Sprintf("flow: node %q: duration %q: want a duration such as \"30s\"", id, text), map[string]any{"node": id})
	}
	if duration < 0 {
		return 0, invalid(fmt.Sprintf("flow: node %q: duration %q is negative", id, text), map[string]any{"node": id})
	}
	return duration, nil
}

func gateSpecOf(node flow.Node, id string) (runtime.GateSpec, error) {
	var spec struct {
		Schema   json.RawMessage `json:"schema"`
		AcceptIf string          `json:"accept_if"`
		Timeout  string          `json:"timeout"`
	}
	if len(bytes.TrimSpace(node.Spec)) > 0 {
		if err := json.Unmarshal(node.Spec, &spec); err != nil {
			return runtime.GateSpec{}, invalid(fmt.Sprintf("flow: node %q: invalid gate spec: want a JSON object", id), map[string]any{"node": id})
		}
	}
	var timeout time.Duration
	if spec.Timeout != "" {
		parsed, err := time.ParseDuration(spec.Timeout)
		if err != nil {
			return runtime.GateSpec{}, invalid(fmt.Sprintf("flow: node %q: timeout %q: want a duration such as \"72h\"", id, spec.Timeout), map[string]any{"node": id})
		}
		if parsed < 0 {
			return runtime.GateSpec{}, invalid(fmt.Sprintf("flow: node %q: timeout %q is negative", id, spec.Timeout), map[string]any{"node": id})
		}
		timeout = parsed
	}
	return runtime.GateSpec{
		Name:     id,
		Schema:   spec.Schema,
		AcceptIf: spec.AcceptIf,
		Timeout:  timeout,
	}, nil
}

func subflowOf(node flow.Node, id string) (runtime.FlowRef, error) {
	var spec struct {
		Flow string `json:"flow"`
		Hash string `json:"hash"`
	}
	if len(bytes.TrimSpace(node.Spec)) > 0 {
		if err := json.Unmarshal(node.Spec, &spec); err != nil {
			return runtime.FlowRef{}, invalid(fmt.Sprintf("flow: node %q: invalid subflow spec: want a JSON object", id), map[string]any{"node": id})
		}
	}
	if spec.Flow == "" {
		return runtime.FlowRef{}, invalid(fmt.Sprintf("flow: node %q: subflow spec has no flow name", id), map[string]any{"node": id})
	}
	return runtime.FlowRef{Name: spec.Flow, Hash: spec.Hash}, nil
}

func stringAt(spec map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := spec[key]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func sortedNodeIDs(def *flow.IR) []string {
	ids := make([]string, 0, len(def.Nodes))
	for id := range def.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func invalid(message string, details map[string]any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, message, details)
}
