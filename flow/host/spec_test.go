package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
)

func TestSpecDecodeErrorsDoNotLeakParserText(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
	}{
		{
			name: "tool",
			run: func() error {
				_, err := nameOf(flow.Node{Kind: "tool", Spec: json.RawMessage(`"not-an-object"`)}, "w")
				return err
			},
		},
		{
			name: "task",
			run: func() error {
				_, err := nameOf(flow.Node{Kind: "task", Spec: json.RawMessage(`[]`)}, "w")
				return err
			},
		},
		{
			name: "wait",
			run: func() error {
				_, err := durationOf(flow.Node{Kind: "wait", Spec: json.RawMessage(`"oops"`)}, "w")
				return err
			},
		},
		{
			name: "wait_duration",
			run: func() error {
				_, err := durationOf(flow.Node{Kind: "wait", Spec: json.RawMessage(`{"duration":"not-a-duration"}`)}, "w")
				return err
			},
		},
		{
			name: "gate",
			run: func() error {
				_, err := gateSpecOf(flow.Node{Kind: "gate", Spec: json.RawMessage(`"bad"`)}, "g")
				return err
			},
		},
		{
			name: "gate_timeout",
			run: func() error {
				_, err := gateSpecOf(flow.Node{Kind: "gate", Spec: json.RawMessage(`{"schema":"s","timeout":"nope"}`)}, "g")
				return err
			},
		},
		{
			name: "subflow",
			run: func() error {
				_, err := subflowOf(flow.Node{Kind: "subflow", Spec: json.RawMessage(`1`)}, "s")
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("code %v (%v), want invalid_argument", core.CodeOf(err), err)
			}
			msg := err.Error()
			if strings.Contains(msg, "json:") || strings.Contains(msg, "cannot unmarshal") {
				t.Fatalf("message leaks parser text: %q", msg)
			}
		})
	}
}
