package ir

import (
	"encoding/json"
	"testing"
)

func TestStateProperties(t *testing.T) {
	tests := []struct {
		name      string
		schema    string
		wantKeys  []string
		wantEvery bool
	}{
		{
			name:     "top-level properties",
			schema:   `{"type":"object","properties":{"plan":{},"tests":{}}}`,
			wantKeys: []string{"plan", "tests"},
		},
		{
			name:     "local $ref root",
			schema:   `{"$defs":{"s":{"type":"object","properties":{"messages":{"type":"array"}}}},"$ref":"#/$defs/s"}`,
			wantKeys: []string{"messages"},
		},
		{
			name:     "allOf composition",
			schema:   `{"type":"object","properties":{"tests":{"type":"object"}},"allOf":[{"type":"object","properties":{"files":{"type":"array"}}}]}`,
			wantKeys: []string{"tests", "files"},
		},
		{
			name:      "boolean true schema",
			schema:    `true`,
			wantEvery: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			keys, every := StateProperties(json.RawMessage(test.schema))
			if every != test.wantEvery {
				t.Fatalf("everyObject %v, want %v", every, test.wantEvery)
			}
			if test.wantEvery {
				return
			}
			for _, key := range test.wantKeys {
				if !keys[key] {
					t.Fatalf("key %q missing from %v", key, keys)
				}
			}
		})
	}
}
