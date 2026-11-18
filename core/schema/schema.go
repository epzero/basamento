package schema

import (
	"encoding/json"

	js "github.com/santhosh-tekuri/jsonschema/v6"
)

// Schema is a JSON Schema document, usually generated from a Go type.
type Schema struct {
	Raw json.RawMessage
}

// Set is the closed world of schemas a document may refer to.
type Set struct {
	schemas map[string]json.RawMessage
}

// NewSet returns an empty schema set.
func NewSet() *Set {
	return &Set{schemas: map[string]json.RawMessage{}}
}

// Add registers a schema by id.
func (s *Set) Add(id string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return invalidArg("empty schema", nil)
	}
	s.schemas[id] = append(json.RawMessage(nil), raw...)
	return nil
}

// Compile compiles the schema id from the set.
func (s *Set) Compile(id string) (*Compiled, error) {
	return compileSet(s.schemas, id)
}

// Compile parses and compiles a single schema with no external references.
func Compile(raw json.RawMessage) (*Compiled, error) {
	return compileSet(map[string]json.RawMessage{inlineSchemaID: raw}, inlineSchemaID)
}

const inlineSchemaID = "urn:basamento:inline"

// Compiled is a schema ready to validate instances against.
type Compiled struct {
	id  string
	sch *js.Schema
}

// Validate checks instance against the compiled schema.
func (c *Compiled) Validate(instance json.RawMessage) error {
	if c == nil || c.sch == nil {
		return invalidArg("no compiled schema", nil)
	}
	v, err := decodeInstance(instance)
	if err != nil {
		return err
	}
	if err := c.sch.Validate(v); err != nil {
		return validationFailure(err)
	}
	return nil
}
