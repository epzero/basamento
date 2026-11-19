package ir

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/flow"
)

// Start and Fail are the reserved entry source and failure target of the edge
// list. They are edge endpoints only, never declared nodes (REQ-FLOW-039,
// REQ-FLOW-040).
const (
	Start = "START"
	Fail  = "FAIL"
)

// Decode parses a flow definition from JSON, or from YAML when data does not
// start with a JSON object. Unknown fields and duplicate object keys are
// rejected: a misspelled field must not silently drop nodes or edges. A stream
// carrying more than one document is rejected too, because only the first
// would be compiled and hashed.
//
// A YAML flow-style mapping such as {name: x} also starts with "{", so input
// that looks like JSON but does not parse as JSON is retried as YAML before the
// JSON error is reported.
func Decode(data []byte) (*flow.IR, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, invalid("flow: empty definition", nil)
	}
	if !isJSON(data) {
		converted, err := yamlToJSON(data)
		if err != nil {
			return nil, err
		}
		return decodeObject(converted)
	}
	f, err := decodeObject(data)
	if err == nil || !startsWithBrace(data) {
		return f, err
	}
	if converted, yamlErr := yamlToJSON(data); yamlErr == nil {
		if f, yamlErr := decodeObject(converted); yamlErr == nil {
			return f, nil
		}
	}
	return nil, err
}

// decodeObject requires a top-level JSON object and checks its keys. A
// top-level null, array or scalar is not a definition.
func decodeObject(text []byte) (*flow.IR, error) {
	if !startsWithBrace(text) {
		return nil, invalid("flow: the definition must be a JSON object", nil)
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.DisallowUnknownFields()
	var f flow.IR
	if err := dec.Decode(&f); err != nil {
		return nil, invalid("flow: invalid JSON: "+err.Error(), nil)
	}
	if _, err := dec.Token(); err != io.EOF {
		if err != nil {
			return nil, invalid("flow: trailing data after the definition", nil)
		}
		// A well-formed second value: only one document is a definition.
		return nil, multipleDocumentsErr()
	}
	if err := checkDuplicateKeys(text); err != nil {
		return nil, err
	}
	return &f, nil
}

// Canonical returns the RFC 8785 canonical JSON of f: sorted object keys,
// shortest number forms, no insignificant whitespace. Node maps are sorted by
// key and edges keep their declaration order, which is routing order
// (REQ-FLOW-001, REQ-FLOW-011).
func Canonical(f *flow.IR) ([]byte, error) {
	if f == nil {
		return nil, invalid("flow: nil definition", nil)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, invalid("flow: "+err.Error(), nil)
	}
	canonical, err := schema.Canonicalize(raw)
	if err != nil {
		return nil, withLocation(err, "flow: ")
	}
	return canonical, nil
}

// Hash returns the lowercase hex SHA-256 of the canonical IR. It is the
// definition identity a run pins (REQ-FLOW-005).
func Hash(f *flow.IR) (string, error) {
	canonical, err := Canonical(f)
	if err != nil {
		return "", err
	}
	return HashCanonical(canonical), nil
}

// HashCanonical returns the lowercase hex SHA-256 of already canonical IR
// bytes, as returned by Canonical.
func HashCanonical(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func isJSON(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

func startsWithBrace(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func yamlToJSON(data []byte) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, invalid("flow: empty definition", nil)
		}
		return nil, invalid("flow: invalid YAML: "+err.Error(), nil)
	}
	if len(doc.Content) == 0 {
		return nil, invalid("flow: empty definition", nil)
	}
	// A decoder over the stream, not yaml.Unmarshal: Unmarshal silently keeps
	// only the first document, so a corrected definition appended after a
	// stale one would be dropped and the stale one hashed instead.
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, multipleDocumentsErr()
	}
	if err := checkYAMLDuplicateKeys(doc.Content[0], ""); err != nil {
		return nil, err
	}
	var value any
	if err := doc.Content[0].Decode(&value); err != nil {
		return nil, invalid("flow: invalid YAML: "+err.Error(), nil)
	}
	converted, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("flow: invalid YAML: "+err.Error(), nil)
	}
	return converted, nil
}

// multipleDocumentsErr rejects a stream that carries more than one document:
// exactly one document is a definition, so the rest would be dropped and the
// first hashed and pinned as if it were the whole input (REQ-FLOW-005).
func multipleDocumentsErr() *core.Error {
	return invalid("flow: more than one document in the definition", nil)
}

func checkYAMLDuplicateKeys(n *yaml.Node, path string) error {
	switch n.Kind {
	case yaml.MappingNode:
		seen := make(map[string]bool, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if isMergeKey(key, n.Content[i].Tag) {
				return mergeKeyErr(path)
			}
			if seen[key] {
				return duplicateErr(path, key)
			}
			seen[key] = true
			if err := checkYAMLDuplicateKeys(n.Content[i+1], path+"/"+escapePointer(key)); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, item := range n.Content {
			if err := checkYAMLDuplicateKeys(item, fmt.Sprintf("%s/%d", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkDuplicateKeys(text []byte) error {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	return walkKeys(dec, "")
}

func walkKeys(dec *json.Decoder, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return invalid("flow: invalid JSON: "+err.Error(), nil)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return invalid("flow: invalid JSON: "+err.Error(), nil)
			}
			key, _ := keyTok.(string)
			if isMergeKey(key, "") {
				return mergeKeyErr(path)
			}
			if seen[key] {
				return duplicateErr(path, key)
			}
			seen[key] = true
			if err := walkKeys(dec, path+"/"+escapePointer(key)); err != nil {
				return err
			}
		}
		_, err := dec.Token() // closing '}'
		return err
	case '[':
		for i := 0; dec.More(); i++ {
			if err := walkKeys(dec, fmt.Sprintf("%s/%d", path, i)); err != nil {
				return err
			}
		}
		_, err := dec.Token() // closing ']'
		return err
	}
	return nil
}

// mergeKey is YAML's merge key. Decode expands it, so a key or a duplicate
// merged through it would bypass the duplicate-key check; it is refused on the
// JSON path too, where nothing expands it and a field named "<<" would
// otherwise survive as data.
const mergeKey = "<<"

func isMergeKey(name, tag string) bool { return name == mergeKey || tag == "!!merge" }

func mergeKeyErr(path string) error {
	where := path
	if where == "" {
		where = "/"
	}
	return invalid(fmt.Sprintf("flow: merge key %q at %s is not allowed", mergeKey, where),
		map[string]any{"location": path + "/" + escapePointer(mergeKey)})
}

func duplicateErr(path, key string) error {
	location := path + "/" + escapePointer(key)
	msg := fmt.Sprintf("flow: duplicate key %q at %s", key, path)
	if path == "/nodes" {
		msg = fmt.Sprintf("flow: duplicate node id %q", key)
	}
	return invalid(msg, map[string]any{"location": location})
}

func escapePointer(token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
}

// withLocation keeps the schema package's JSON Pointer, if any, on the error.
func withLocation(err error, prefix string) error {
	var pe *core.Error
	if errors.As(err, &pe) {
		return invalid(prefix+pe.Message, pe.Details)
	}
	return invalid(prefix+err.Error(), nil)
}

func invalid(msg string, details map[string]any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, msg, details)
}
