package schema

import "encoding/json"

// Marshal JSON-encodes v and returns its canonical form.
func Marshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, invalidArgf("marshal value: %v", err)
	}
	return Canonicalize(raw)
}
