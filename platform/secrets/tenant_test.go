package secrets

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestValidateTenantREQCORE005(t *testing.T) {
	t.Parallel()
	valid := []string{"acme", "ACME", "a", "a.b", "a..b", "tenant-with-dashes", "tenant_with_underscores", "tenant.with.dots"}
	for _, id := range valid {
		if err := ValidateTenant(id); err != nil {
			t.Fatalf("%q valid: %v", id, err)
		}
	}
	invalid := []struct {
		id string
	}{
		{""},
		{"."},
		{".."},
		{"acme/sub"},
		{"acme\\sub"},
		{" acme"},
		{"acme "},
		{"acme\x00evil"},
		{"acme\x1f"},
		{"acme\x7f"},
	}
	for _, tc := range invalid {
		if err := ValidateTenant(tc.id); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("%q: err=%v", tc.id, err)
		}
	}
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'x'
	}
	if err := ValidateTenant(string(long)); err == nil {
		t.Fatal("long tenant accepted")
	}
}
