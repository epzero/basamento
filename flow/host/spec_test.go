package host

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
)

func assertSpecDecodeError(t *testing.T, err error, node, wantMsg string) {
	t.Helper()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %T %v, want *core.Error", err, err)
	}
	if pe.Code != core.CodeInvalidArgument {
		t.Fatalf("code %s, want invalid_argument", pe.Code)
	}
	if pe.Message != wantMsg {
		t.Fatalf("message %q, want %q", pe.Message, wantMsg)
	}
	if got, _ := pe.Details["node"].(string); got != node {
		t.Fatalf("details node %q, want %q", got, node)
	}
	if err.Error() != string(pe.Code)+": "+wantMsg {
		t.Fatalf("Error() %q, want %q", err.Error(), string(pe.Code)+": "+wantMsg)
	}
}

func TestSpecDecodeErrors(t *testing.T) {
	cases := []struct {
		name string
		node string
		want string
		run  func() error
	}{
		{
			name: "tool",
			node: "w",
			want: `flow: node "w": invalid tool spec: want a JSON object with a string "tool"`,
			run: func() error {
				_, err := nameOf(flow.Node{Kind: "tool", Spec: json.RawMessage(`"not-an-object"`)}, "w")
				return err
			},
		},
		{
			name: "task",
			node: "w",
			want: `flow: node "w": invalid task spec: want a JSON object with a string "task"`,
			run: func() error {
				_, err := nameOf(flow.Node{Kind: "task", Spec: json.RawMessage(`[]`)}, "w")
				return err
			},
		},
		{
			name: "wait",
			node: "w",
			want: `flow: node "w": invalid wait spec: want a JSON object`,
			run: func() error {
				_, err := durationOf(flow.Node{Kind: "wait", Spec: json.RawMessage(`"oops"`)}, "w")
				return err
			},
		},
		{
			name: "wait_duration",
			node: "w",
			want: `flow: node "w": duration "not-a-duration": want a duration such as "30s"`,
			run: func() error {
				_, err := durationOf(flow.Node{Kind: "wait", Spec: json.RawMessage(`{"duration":"not-a-duration"}`)}, "w")
				return err
			},
		},
		{
			name: "wait_duration_not_string",
			node: "w",
			want: `flow: node "w": wait spec has no duration`,
			run: func() error {
				_, err := durationOf(flow.Node{Kind: "wait", Spec: json.RawMessage(`{"duration":30}`)}, "w")
				return err
			},
		},
		{
			name: "gate",
			node: "g",
			want: `flow: node "g": invalid gate spec: want a JSON object`,
			run: func() error {
				_, err := gateSpecOf(flow.Node{Kind: "gate", Spec: json.RawMessage(`"bad"`)}, "g")
				return err
			},
		},
		{
			name: "gate_wrong_shape",
			node: "g",
			want: `flow: node "g": invalid gate spec: want a JSON object`,
			run: func() error {
				_, err := gateSpecOf(flow.Node{Kind: "gate", Spec: json.RawMessage(`{"schema":"s","timeout":["1h"]}`)}, "g")
				return err
			},
		},
		{
			name: "gate_timeout",
			node: "g",
			want: `flow: node "g": timeout "nope": want a duration such as "72h"`,
			run: func() error {
				_, err := gateSpecOf(flow.Node{Kind: "gate", Spec: json.RawMessage(`{"schema":"s","timeout":"nope"}`)}, "g")
				return err
			},
		},
		{
			name: "subflow",
			node: "s",
			want: `flow: node "s": invalid subflow spec: want a JSON object`,
			run: func() error {
				_, err := subflowOf(flow.Node{Kind: "subflow", Spec: json.RawMessage(`1`)}, "s")
				return err
			},
		},
		{
			name: "subflow_no_flow",
			node: "s",
			want: `flow: node "s": subflow spec has no flow name`,
			run: func() error {
				_, err := subflowOf(flow.Node{Kind: "subflow", Spec: json.RawMessage(`{"hash":"pin"}`)}, "s")
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSpecDecodeError(t, tc.run(), tc.node, tc.want)
		})
	}
}
