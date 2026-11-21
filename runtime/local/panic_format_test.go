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

	errVal := errors.New("boom")
	if got := formatPanicValue(errVal); got != "boom" {
		t.Fatalf("error value %q", got)
	}
	gotInt := formatPanicValue(42)
	if gotInt == formatPanicValue("42") {
		t.Fatal("int must format through fmt, not as a string")
	}
	if !strings.HasSuffix(gotInt, "... (truncated)") {
		t.Fatalf("int fmt width exceeds rune cap, got len %d", len(gotInt))
	}
}
