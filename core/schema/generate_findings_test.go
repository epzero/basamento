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
		X int `json:"x"`
		y int `json:"y"`
	}
	_, err := ForType(reflect.TypeOf(s{}))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
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
