package schema

import (
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf16"
)

// decodeInstance parses JSON with UseNumber and rejects duplicate object keys.
func decodeInstance(data []byte) (any, error) {
	dec := json.NewDecoder(bytesReader(data))
	dec.UseNumber()
	var stack []string
	v, err := decodeValue(dec, &stack)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, invalidArg("trailing JSON", map[string]any{"location": ""})
		}
		return nil, invalidArgf("parse instance: %v", err)
	}
	return v, nil
}

func decodeValue(dec *json.Decoder, stack *[]string) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return decodeObject(dec, stack)
		case '[':
			return decodeArray(dec, stack)
		default:
			return nil, invalidArgf("unexpected delimiter %v", v)
		}
	case json.Number:
		return v, nil
	default:
		return v, nil
	}
}

func decodeObject(dec *json.Decoder, stack *[]string) (map[string]any, error) {
	seen := map[string]struct{}{}
	out := map[string]any{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, invalidArgf("object key is %T", keyTok)
		}
		if _, dup := seen[key]; dup {
			loc := ptrJoin(append(*stack, key))
			return nil, invalidArg("duplicate object member", map[string]any{"location": loc})
		}
		seen[key] = struct{}{}
		*stack = append(*stack, key)
		val, err := decodeValue(dec, stack)
		*stack = (*stack)[:len(*stack)-1]
		if err != nil {
			return nil, err
		}
		out[key] = val
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeArray(dec *json.Decoder, stack *[]string) ([]any, error) {
	var out []any
	i := 0
	for dec.More() {
		*stack = append(*stack, fmt.Sprint(i))
		val, err := decodeValue(dec, stack)
		*stack = (*stack)[:len(*stack)-1]
		if err != nil {
			return nil, err
		}
		out = append(out, val)
		i++
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

func ptrJoin(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	var b []byte
	for _, p := range parts {
		b = append(b, '/')
		b = appendEscapedKey(b, p)
	}
	return string(b)
}

func appendEscapedKey(b []byte, key string) []byte {
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch c {
		case '~':
			b = append(b, '~', '0')
		case '/':
			b = append(b, '~', '1')
		default:
			b = append(b, c)
		}
	}
	return b
}

func bytesReader(b []byte) *sliceReader { return &sliceReader{b: b} }

type sliceReader struct {
	b []byte
	i int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// hasUnpairedSurrogate reports whether s contains an unpaired UTF-16 surrogate.
func hasUnpairedSurrogate(s string) bool {
	for _, r := range s {
		if utf16.IsSurrogate(r) {
			return true
		}
	}
	return false
}
