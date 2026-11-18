package schema

import (
	"reflect"
	"testing"
)

func TestForTypeNil(t *testing.T) {
	_, err := ForType(nil)
	if err == nil {
		t.Fatal("want error")
	}
}

func TestForAnyRejected(t *testing.T) {
	_, err := For[any]()
	if err == nil {
		t.Fatal("want error")
	}
}

func TestWalkInvalidUnexportedField(t *testing.T) {
	type s struct {
		X int
		y int
	}
	if _, ok := walkInvalidField(reflect.TypeOf(s{}), "", map[reflect.Type]bool{}); ok {
		t.Fatal("unexported field should not block schema for exported fields")
	}
}

func TestJSONNameOmitOnlyTag(t *testing.T) {
	type s struct {
		Hidden int `json:",omitempty"`
		X      int `json:"x"`
	}
	if got := jsonName(reflect.TypeOf(s{}).Field(0)); got != "Hidden" {
		t.Fatalf("jsonName %q", got)
	}
}
