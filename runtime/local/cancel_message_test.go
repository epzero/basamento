package local

import "testing"

func TestCancelMessageBoundsReason(t *testing.T) {
	long := make([]byte, 10_000)
	for i := range long {
		long[i] = 'x'
	}
	got := cancelMessage(string(long))
	if len(got) > len("run cancelled: ")+256 {
		t.Fatalf("message len %d, want at most %d", len(got), len("run cancelled: ")+256)
	}
}

func TestCancelMessageRuneBoundary(t *testing.T) {
	// 255 ASCII bytes plus one 3-byte rune; cut at 256 bytes must not split the rune.
	prefix := make([]byte, 255)
	for i := range prefix {
		prefix[i] = 'a'
	}
	reason := string(prefix) + "€extra"
	got := cancelMessage(reason)
	if got != "run cancelled: "+string(prefix)+"€" {
		t.Fatalf("got %q", got)
	}
}

func TestCancelMessageEmptyAndExact(t *testing.T) {
	if got := cancelMessage(""); got != "run cancelled" {
		t.Fatalf("empty: %q", got)
	}
	exact := make([]byte, 256)
	for i := range exact {
		exact[i] = 'b'
	}
	want := "run cancelled: " + string(exact)
	if got := cancelMessage(string(exact)); got != want {
		t.Fatalf("exact 256: len %d want %d", len(got), len(want))
	}
}
