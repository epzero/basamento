package steps

import (
	"errors"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/schema"
)

func TestInstanceRejectedRequiresViolations(t *testing.T) {
	malformed := core.NewError(core.CodeInvalidArgument, "decode instance", nil)
	if instanceRejected(malformed) {
		t.Fatal("malformed instance must not count as rejected")
	}
	withViolations := core.NewError(core.CodeInvalidArgument, "instance does not match schema", map[string]any{
		"violations": []schema.Violation{{Location: "/x", Keyword: "type"}},
	})
	if !instanceRejected(withViolations) {
		t.Fatal("validation failure with violations must count as rejected")
	}
}

func TestInstanceVerdictError(t *testing.T) {
	malformed := core.NewError(core.CodeInvalidArgument, "decode instance", nil)
	withViolations := core.NewError(core.CodeInvalidArgument, "instance does not match schema", map[string]any{
		"violations": []schema.Violation{{Location: "/x", Keyword: "type"}},
	})
	tests := []struct {
		name    string
		verdict string
		err     error
		want    string
		wantErr bool
	}{
		{name: "accepted with nothing", verdict: "accepted", want: ""},
		{name: "accepted with an error", verdict: "accepted", err: malformed, want: "accepted", wantErr: true},
		{name: "rejected without an error", verdict: "rejected", want: "accepted", wantErr: true},
		{name: "rejected with a malformed instance", verdict: "rejected", err: malformed, want: "rejected without a violation", wantErr: true},
		{name: "rejected with violations", verdict: "rejected", err: withViolations, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := instanceVerdictError(tc.verdict, tc.err)
			if !tc.wantErr {
				if got != nil {
					t.Fatalf("error %v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(got.Error(), tc.want) {
				t.Fatalf("message %q does not contain %q", got, tc.want)
			}
			if tc.err != nil && !errors.Is(got, tc.err) {
				t.Fatalf("message %q does not wrap the underlying error", got)
			}
		})
	}
}
