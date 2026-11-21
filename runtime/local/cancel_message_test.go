package local

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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
	// 255 ASCII runes plus one 3-byte rune; cut at 256 runes must not split the rune.
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

func TestCancelMessage300EuroSigns(t *testing.T) {
	reason := strings.Repeat("€", 300)
	got := cancelMessage(reason)
	want := "run cancelled: " + strings.Repeat("€", 256)
	if got != want {
		t.Fatalf("got len %d runes %d, want 256 euros", len(got), utf8.RuneCountInString(got))
	}
}

func TestCancelMessageInvalidUTF8InCutRegion(t *testing.T) {
	reason := strings.Repeat("a", 255) + "\xff" + strings.Repeat("b", 100)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("cancelMessage panicked: %v", r)
		}
	}()
	_ = cancelMessage(reason)
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
