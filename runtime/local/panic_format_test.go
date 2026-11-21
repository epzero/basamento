package local

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	stackTruncMarker = "... (stack truncated)"
	// stackLogCap matches maxPanicStack; pinned so tests catch a raised production cap.
	stackLogCap = 16 << 10
)

func TestFormatPanicStackTruncates(t *testing.T) {
	long := bytes.Repeat([]byte("s"), stackLogCap+500)
	got := formatPanicStack(long)
	wantLen := stackLogCap + len(stackTruncMarker)
	if len(got) != wantLen {
		t.Fatalf("len %d, want %d", len(got), wantLen)
	}
	if !strings.HasSuffix(got, stackTruncMarker) {
		t.Fatal("missing stack truncation marker")
	}
	if got[:stackLogCap] != string(long[:stackLogCap]) {
		t.Fatal("prefix bytes changed")
	}

	exact := bytes.Repeat([]byte("x"), stackLogCap)
	gotExact := formatPanicStack(exact)
	if gotExact != string(exact) {
		t.Fatalf("exact cap: got len %d, want %d unchanged", len(gotExact), stackLogCap)
	}
	if strings.Contains(gotExact, stackTruncMarker) {
		t.Fatal("exact cap must not get marker")
	}

	short := []byte("short stack")
	if got := formatPanicStack(short); got != string(short) {
		t.Fatalf("short stack: got %q", got)
	}
}

type stringerVal struct{ msg string }

func (s stringerVal) String() string { return s.msg }

type errPanics struct{}

func (errPanics) Error() string { panic("inside Error") }

func TestFormatPanicValueBounds(t *testing.T) {
	exact := strings.Repeat("a", maxPanicValue)
	if got := formatPanicValue(exact); got != exact {
		t.Fatalf("exact runes: got len %d", utf8.RuneCountInString(got))
	}

	over := strings.Repeat("a", maxPanicValue+1)
	got := formatPanicValue(over)
	if !strings.HasSuffix(got, "... (truncated)") {
		t.Fatal("over runes: missing marker")
	}
	body := strings.TrimSuffix(got, "... (truncated)")
	if utf8.RuneCountInString(body) != maxPanicValue {
		t.Fatalf("body runes %d, want %d", utf8.RuneCountInString(body), maxPanicValue)
	}

	overUTF8 := strings.Repeat("é", maxPanicValue+10)
	got = formatPanicValue(overUTF8)
	if !utf8.ValidString(got) {
		t.Fatal("multibyte cut: invalid UTF-8")
	}
	body = strings.TrimSuffix(got, "... (truncated)")
	if utf8.RuneCountInString(body) != maxPanicValue {
		t.Fatalf("multibyte body runes %d, want %d", utf8.RuneCountInString(body), maxPanicValue)
	}

	if got := formatPanicValue(errors.New("boom")); got != "boom" {
		t.Fatalf("error value %q", got)
	}
	if got := formatPanicValue(42); got != "42" {
		t.Fatalf("int value %q", got)
	}
	if got := formatPanicValue(struct{ N int }{42}); got != "{42}" {
		t.Fatalf("struct value %q", got)
	}
	if got := formatPanicValue(stringerVal{msg: "from-stringer"}); got != "from-stringer" {
		t.Fatalf("Stringer %q", got)
	}
	func() {
		defer func() {
			if recover() != nil {
				t.Fatal("formatPanicValue must not panic when Error panics")
			}
		}()
		got := formatPanicValue(errPanics{})
		if !strings.Contains(got, "PANIC=") {
			t.Fatalf("Error panic formatting %q, want PANIC=", got)
		}
	}()

	slice := []string{strings.Repeat("a", maxPanicValue*2)}
	got = formatPanicValue(slice)
	if !strings.HasSuffix(got, "... (truncated)") {
		t.Fatal("slice: missing marker")
	}
	body = strings.TrimSuffix(got, "... (truncated)")
	if utf8.RuneCountInString(body) != maxPanicValue {
		t.Fatalf("slice body runes %d, want %d", utf8.RuneCountInString(body), maxPanicValue)
	}
	// Sanity: fmt stringifies the slice without zero-padding element strings.
	if !strings.HasPrefix(body, "[") {
		t.Fatalf("slice body should look formatted, got prefix %q", body[:min(8, len(body))])
	}
}
