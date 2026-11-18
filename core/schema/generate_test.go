package schema

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/epzero/basamento/core"
)

func compileForType(t *testing.T, sample any) *Compiled {
	t.Helper()
	s, err := ForType(reflect.TypeOf(sample))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s.Raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGeneratedRootNullRejected(t *testing.T) {
	type S struct {
		X int `json:"x"`
	}
	c := compileForType(t, S{})
	if err := c.Validate(json.RawMessage(`null`)); err == nil {
		t.Fatal("root null accepted")
	}
}

func TestGeneratedNestedStructNullRejected(t *testing.T) {
	type Inner struct {
		A int `json:"a"`
	}
	type Outer struct {
		In Inner `json:"in"`
	}
	c := compileForType(t, Outer{})
	if err := c.Validate(json.RawMessage(`{"in":null}`)); err == nil {
		t.Fatal("nested struct null accepted")
	}
}

func TestNestedSliceAcceptsNull(t *testing.T) {
	type Inner struct {
		Tags []string `json:"tags"`
	}
	type Outer struct {
		In Inner `json:"in"`
	}
	c := compileForType(t, Outer{})
	if err := c.Validate(json.RawMessage(`{"in":{"tags":null}}`)); err != nil {
		t.Fatalf("want accept null nested slice: %v", err)
	}
}

func TestGeneratedNestedMapNullAtDepth3(t *testing.T) {
	type Leaf struct {
		Scores map[string]int `json:"scores"`
	}
	type Mid struct {
		Leaf Leaf `json:"leaf"`
	}
	type Root struct {
		Mid Mid `json:"mid"`
	}
	c := compileForType(t, Root{})
	if err := c.Validate(json.RawMessage(`{"mid":{"leaf":{"scores":null}}}`)); err != nil {
		t.Fatalf("depth-3 map null: %v", err)
	}
}

func TestGeneratedPointerFieldNullAccepted(t *testing.T) {
	type S struct {
		P *int `json:"p"`
	}
	c := compileForType(t, S{})
	if err := c.Validate(json.RawMessage(`{"p":null}`)); err != nil {
		t.Fatalf("pointer null: %v", err)
	}
}

func TestGeneratedPointerStructFieldNullAccepted(t *testing.T) {
	type Inner struct {
		A int `json:"a"`
	}
	type Outer struct {
		In *Inner `json:"in"`
	}
	c := compileForType(t, Outer{})
	if err := c.Validate(json.RawMessage(`{"in":null}`)); err != nil {
		t.Fatalf("pointer struct null: %v", err)
	}
}

func TestGeneratedMapOfSlicesAcceptsNull(t *testing.T) {
	type Root struct {
		Groups map[string][]string `json:"groups"`
	}
	c := compileForType(t, Root{})
	if err := c.Validate(json.RawMessage(`{"groups":null}`)); err != nil {
		t.Fatalf("map null: %v", err)
	}
	if err := c.Validate(json.RawMessage(`{"groups":{"x":null}}`)); err != nil {
		t.Fatalf("nested slice null in map: %v", err)
	}
}

func TestGeneratedSliceOfStructNestedSliceNull(t *testing.T) {
	type Row struct {
		Tags []string `json:"tags"`
	}
	type Table struct {
		Rows []Row `json:"rows"`
	}
	c := compileForType(t, Table{})
	if err := c.Validate(json.RawMessage(`{"rows":[{"tags":null}]}`)); err != nil {
		t.Fatalf("slice of struct nested slice null: %v", err)
	}
}

func TestGeneratedStructSliceElementNullRejected(t *testing.T) {
	type Inner struct {
		A int `json:"a"`
	}
	type Outer struct {
		Items []Inner `json:"items"`
	}
	c := compileForType(t, Outer{})
	err := c.Validate(json.RawMessage(`{"items":[null]}`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("want invalid_argument for [null] element: %v", err)
	}
}
