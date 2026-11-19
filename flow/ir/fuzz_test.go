package ir

import (
	"bytes"
	"testing"
)

// FuzzDecode checks that any definition the decoder accepts has a stable
// canonical form, which the content hash relies on (REQ-FLOW-005).
func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"name":"x","version":1,"state":{"schema":{"type":"object"}},"nodes":{},"edges":[]}`))
	f.Add([]byte("name: x\nversion: 1\nstate:\n  schema:\n    type: object\n"))
	f.Add([]byte(`not a definition`))

	f.Fuzz(func(t *testing.T, data []byte) {
		definition, err := Decode(data)
		if err != nil {
			return
		}
		first, err := Canonical(definition)
		if err != nil {
			t.Fatalf("decoded definition does not canonicalize: %v", err)
		}
		second, err := Canonical(definition)
		if err != nil || !bytes.Equal(first, second) {
			t.Fatalf("canonical form is not stable: %v", err)
		}
	})
}
