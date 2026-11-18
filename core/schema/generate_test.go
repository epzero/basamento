package schema

import (
	"encoding/json"
	"testing"
)

func TestNestedSliceAcceptsNull(t *testing.T) {
	type Inner struct {
		Tags []string `json:"tags"`
	}
	type Outer struct {
		In Inner `json:"in"`
	}
	s, err := For[Outer]()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(json.RawMessage(`{"in":{"tags":null}}`)); err != nil {
		t.Fatalf("want accept null nested slice: %v", err)
	}
}
