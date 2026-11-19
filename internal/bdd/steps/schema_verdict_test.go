package steps

import (
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
