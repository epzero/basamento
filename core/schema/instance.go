package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// decodeInstance parses JSON with UseNumber and rejects duplicate object keys.
func decodeInstance(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var stack []string
	v, err := decodeValue(dec, &stack, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, invalidArg("trailing JSON", map[string]any{"location": ""})
		}
		return nil, wrapDecodeErr(err, "")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder, stack *[]string, depth int) (any, error) {
	if depth > maxNestingDepth {
		return nil, invalidArg("nesting depth exceeded", map[string]any{"location": ptrJoin(*stack)})
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, wrapDecodeErr(err, ptrJoin(*stack))
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return decodeObject(dec, stack, depth+1)
		case '[':
			return decodeArray(dec, stack, depth+1)
		default:
			return nil, invalidArgf("unexpected delimiter %v", v)
		}
	case json.Number:
		if err := validateJSONNumberString(string(v)); err != nil {
			return nil, invalidArg("invalid number", map[string]any{"location": ptrJoin(*stack)})
		}
		return v, nil
	default:
		return v, nil
	}
}

func decodeObject(dec *json.Decoder, stack *[]string, depth int) (map[string]any, error) {
	seen := map[string]struct{}{}
	out := map[string]any{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, wrapDecodeErr(err, ptrJoin(*stack))
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
		val, err := decodeValue(dec, stack, depth)
		*stack = (*stack)[:len(*stack)-1]
		if err != nil {
			return nil, err
		}
		out[key] = val
	}
	if _, err := dec.Token(); err != nil {
		return nil, wrapDecodeErr(err, ptrJoin(*stack))
	}
	return out, nil
}

func decodeArray(dec *json.Decoder, stack *[]string, depth int) ([]any, error) {
	var out []any
	i := 0
	for dec.More() {
		*stack = append(*stack, fmt.Sprint(i))
		val, err := decodeValue(dec, stack, depth)
		*stack = (*stack)[:len(*stack)-1]
		if err != nil {
			return nil, err
		}
		out = append(out, val)
		i++
	}
	if _, err := dec.Token(); err != nil {
		return nil, wrapDecodeErr(err, ptrJoin(*stack))
	}
	return out, nil
}

func wrapDecodeErr(err error, loc string) error {
	if errors.Is(err, io.EOF) {
		return invalidArg("unexpected end of JSON", map[string]any{"location": loc})
	}
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		return invalidArgf("parse instance: %v", syn)
	}
	return invalidArgf("parse instance: %v", err)
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

// hasUnpairedSurrogate reports whether s contains an unpaired UTF-16 surrogate.
func hasUnpairedSurrogate(s string) bool {
	for _, r := range s {
		if utf16.IsSurrogate(r) {
			return true
		}
	}
	return false
}