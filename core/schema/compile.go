package schema

import (
	"encoding/json"
	"errors"
	"strings"

	js "github.com/santhosh-tekuri/jsonschema/v6"
)

type closedLoader struct{}

func (closedLoader) Load(url string) (any, error) {
	return nil, &js.LoadURLError{URL: url, Err: errors.New("not in schema set")}
}

func newCompiler() *js.Compiler {
	c := js.NewCompiler()
	c.DefaultDraft(js.Draft2020)
	c.AssertFormat()
	c.UseLoader(closedLoader{})
	return c
}

func compileSet(schemas map[string]json.RawMessage, rootID string) (*Compiled, error) {
	c := newCompiler()
	for id, raw := range schemas {
		if err := validateRefsClosed(id, raw, schemas); err != nil {
			return nil, err
		}
		doc, err := js.UnmarshalJSON(strings.NewReader(string(raw)))
		if err != nil {
			return nil, invalidArgf("parse schema %s: %v", id, err)
		}
		if err := c.AddResource(id, doc); err != nil {
			return nil, invalidArgf("add schema %s: %v", id, err)
		}
	}
	sch, err := c.Compile(rootID)
	if err != nil {
		return nil, compilationFailure(rootID, err)
	}
	stripNonAssertedFormats(sch)
	return &Compiled{id: rootID, sch: sch}, nil
}
