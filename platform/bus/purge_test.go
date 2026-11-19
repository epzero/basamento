package bus

import "testing"

func TestPurgeFloorAcceptedAfter(t *testing.T) {
	const floor uint64 = 10
	for _, after := range []uint64{0, floor, floor + 1} {
		if after > 0 && after < floor {
			t.Fatalf("after %d should be refused", after)
		}
	}
}
