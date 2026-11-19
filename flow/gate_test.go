package flow

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestValidateGatePayload(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"approved": {"type": "boolean"}, "notes": {"type": "string"}},
		"required": ["approved", "notes"]
	}`)
	cases := []struct {
		name     string
		schema   json.RawMessage
		acceptIf string
		payload  string
		code     core.Code
	}{
		{name: "no schema accepts anything", payload: `{"anything": true}`},
		{name: "schema rejects a missing field", schema: schema, payload: `{"approved": true}`, code: core.CodeInvalidArgument},
		{name: "condition rejects", schema: schema, acceptIf: `input.approved || size(input.notes) > 0`, payload: `{"approved": false, "notes": ""}`, code: core.CodeInvalidArgument},
		{name: "condition accepts", schema: schema, acceptIf: `input.approved || size(input.notes) > 0`, payload: `{"approved": false, "notes": "ok"}`},
		{name: "condition over a dynamic input", acceptIf: `input.approved`, payload: `{"approved": true}`},
		{name: "condition returns no boolean", acceptIf: `1 + 1`, payload: `{}`, code: core.CodeInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGatePayload(tc.schema, tc.acceptIf, json.RawMessage(tc.payload))
			if tc.code == "" {
				if err != nil {
					t.Fatalf("err %v, want accepted", err)
				}
				return
			}
			if got := core.CodeOf(err); got != tc.code {
				t.Fatalf("code %s, want %s (%v)", got, tc.code, err)
			}
		})
	}
}
