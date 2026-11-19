package blob

import "testing"

func TestValidateTenantSegment(t *testing.T) {
	t.Parallel()
	ok := []string{"acme", "globex", "tenant-1"}
	for _, tid := range ok {
		if err := validateTenantSegment(tid); err != nil {
			t.Errorf("%q: %v", tid, err)
		}
	}
	bad := []string{"", ".", "..", "a/b", `a\.part`, "a\x00b", string([]byte{127})}
	for _, tid := range bad {
		if err := validateTenantSegment(tid); err == nil {
			t.Errorf("%q should be rejected", tid)
		}
	}
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	if err := validateTenantSegment(string(long)); err == nil {
		t.Fatal("long tenant should fail")
	}
}
