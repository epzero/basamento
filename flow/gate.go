package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/expr"
)

// ValidateGatePayload checks a gate resolution against the gate's schema and
// acceptance condition (REQ-FLOW-006). An empty schema accepts any JSON, and an
// empty condition accepts any payload that matches the schema. A payload that
// fails either check is rejected with code invalid_argument; the caller keeps
// the gate open.
//
// The condition is CEL over `input` (the payload) and `state`. The runtime has
// no flow state at a gate, so `state` is null here: a condition that needs it
// cannot be validated at the runtime and fails closed rather than accepting.
func ValidateGatePayload(schemaRaw json.RawMessage, acceptIf string, payload json.RawMessage) error {
	if hasSchema(schemaRaw) {
		compiled, err := schema.Compile(schemaRaw)
		if err != nil {
			return gateInvalid(fmt.Sprintf("gate schema: %s", err.Error()), nil)
		}
		if err := compiled.Validate(payload); err != nil {
			return gateInvalid(fmt.Sprintf("gate payload: %s", err.Error()), map[string]any{"reason": "schema"})
		}
	}
	condition := strings.TrimSpace(acceptIf)
	if condition == "" {
		return nil
	}
	var inputSchema json.RawMessage
	var resultSchema json.RawMessage
	if hasSchema(schemaRaw) {
		inputSchema = schemaRaw
		// A typed input lets the compiler check the condition returns a bool.
		resultSchema = json.RawMessage(`{"type":"boolean"}`)
	}
	env, err := expr.NewEnv([]expr.Var{
		{Name: "input", Schema: inputSchema},
		{Name: "state", Schema: nil},
	})
	if err != nil {
		return gateInvalid(fmt.Sprintf("gate acceptance condition: %s", err.Error()), map[string]any{"reason": "condition"})
	}
	program, err := env.Compile(condition, resultSchema)
	if err != nil {
		return gateInvalid(fmt.Sprintf("gate acceptance condition: %s", err.Error()), map[string]any{"reason": "condition"})
	}
	var input any
	if len(bytes.TrimSpace(payload)) > 0 {
		if err := json.Unmarshal(payload, &input); err != nil {
			return gateInvalid(fmt.Sprintf("gate payload: %s", err.Error()), map[string]any{"reason": "payload"})
		}
	}
	result, err := program.Eval(map[string]any{"input": input, "state": nil})
	if err != nil {
		return gateInvalid(fmt.Sprintf("gate acceptance condition: %s", err.Error()), map[string]any{"reason": "condition"})
	}
	accepted, ok := result.(bool)
	if !ok {
		return gateInvalid("gate acceptance condition did not return a boolean", map[string]any{"reason": "condition"})
	}
	if !accepted {
		return gateInvalid("gate payload rejected by its acceptance condition", map[string]any{"reason": "rejected"})
	}
	return nil
}

func hasSchema(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func gateInvalid(message string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	return core.NewError(core.CodeInvalidArgument, message, details)
}
