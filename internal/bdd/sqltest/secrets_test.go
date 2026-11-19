package sqltest

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestErrorContainsSecret(t *testing.T) {
	err := core.NewError(core.CodeInternal, "failed", map[string]any{"url": "postgres://u:leak@h/db"})
	if !ErrorContainsSecret(err, "leak") {
		t.Fatal("expected secret in details")
	}
	if ErrorContainsSecret(err, "other") {
		t.Fatal("false positive")
	}
}
