package steps

import (
	"errors"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	operatorservice "go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/serviceerror"
)

func TestTemporalStub_OperatorSearchAttributes(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	list, err := c.OperatorService().ListSearchAttributes(t.Context(), &operatorservice.ListSearchAttributesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if list.GetSystemAttributes()["WorkflowId"] != enumspb.INDEXED_VALUE_TYPE_KEYWORD {
		t.Fatal("system attrs")
	}
	_, err = c.OperatorService().AddSearchAttributes(t.Context(), &operatorservice.AddSearchAttributesRequest{
		SearchAttributes: map[string]enumspb.IndexedValueType{
			"CustomFoo": enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.SearchAttributes()["CustomFoo"] != enumspb.INDEXED_VALUE_TYPE_KEYWORD {
		t.Fatal("custom registered")
	}
	list2, err := c.OperatorService().ListSearchAttributes(t.Context(), &operatorservice.ListSearchAttributesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if list2.GetCustomAttributes()["CustomFoo"] != enumspb.INDEXED_VALUE_TYPE_KEYWORD {
		t.Fatal("list custom")
	}
	_, err = c.OperatorService().AddSearchAttributes(t.Context(), &operatorservice.AddSearchAttributesRequest{
		SearchAttributes: map[string]enumspb.IndexedValueType{
			"CustomFoo": enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		},
	})
	if err == nil {
		t.Fatal("duplicate")
	}
	var ae *serviceerror.AlreadyExists
	if !errors.As(err, &ae) {
		t.Fatalf("type %T", err)
	}
	_, err = c.OperatorService().AddSearchAttributes(t.Context(), &operatorservice.AddSearchAttributesRequest{
		SearchAttributes: map[string]enumspb.IndexedValueType{
			"Bad": enumspb.INDEXED_VALUE_TYPE_UNSPECIFIED,
		},
	})
	if err == nil {
		t.Fatal("unspecified type")
	}
}

func TestTemporalStub_AddSearchAttributesBatchBothOrNeither(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	_, err := c.OperatorService().AddSearchAttributes(t.Context(), &operatorservice.AddSearchAttributesRequest{
		SearchAttributes: map[string]enumspb.IndexedValueType{
			"BatchA": enumspb.INDEXED_VALUE_TYPE_KEYWORD,
			"BatchB": enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sa := stub.SearchAttributes()
	if _, a := sa["BatchA"]; !a {
		t.Fatal("BatchA missing")
	}
	if _, b := sa["BatchB"]; !b {
		t.Fatalf("BatchB must register %#v", sa)
	}
}

func TestTemporalStub_AddSearchAttributesAllOrNothing(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	_, err := c.OperatorService().AddSearchAttributes(t.Context(), &operatorservice.AddSearchAttributesRequest{
		SearchAttributes: map[string]enumspb.IndexedValueType{
			"A":          enumspb.INDEXED_VALUE_TYPE_KEYWORD,
			"WorkflowId": enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		},
	})
	if err == nil {
		t.Fatal("system name must fail")
	}
	var ae *serviceerror.AlreadyExists
	if !errors.As(err, &ae) {
		t.Fatalf("type %T", err)
	}
	if _, ok := stub.SearchAttributes()["A"]; ok {
		t.Fatal("A must not register when WorkflowId fails")
	}
	_, err = c.OperatorService().AddSearchAttributes(t.Context(), &operatorservice.AddSearchAttributesRequest{
		SearchAttributes: map[string]enumspb.IndexedValueType{"DupCustom": enumspb.INDEXED_VALUE_TYPE_KEYWORD},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.OperatorService().AddSearchAttributes(t.Context(), &operatorservice.AddSearchAttributesRequest{
		SearchAttributes: map[string]enumspb.IndexedValueType{
			"DupCustom": enumspb.INDEXED_VALUE_TYPE_KEYWORD,
			"NewOnly":   enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		},
	})
	if err == nil {
		t.Fatal("duplicate custom must fail")
	}
	if !errors.As(err, &ae) {
		t.Fatalf("type %T", err)
	}
	if _, ok := stub.SearchAttributes()["NewOnly"]; ok {
		t.Fatal("NewOnly must not register when DupCustom fails")
	}
}
