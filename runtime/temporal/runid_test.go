package temporal

import (
	"regexp"
	"testing"
)

func TestDefaultNewRunIDFormat(t *testing.T) {
	const pattern = `^run_[0-9a-f]{32}$`
	re := regexp.MustCompile(pattern)
	a, err := defaultNewRunID()
	if err != nil {
		t.Fatalf("defaultNewRunID failed: %v", err)
	}
	if !re.MatchString(a) {
		t.Fatalf("format: got %q want %s", a, pattern)
	}
	if err := validateRunID(a); err != nil {
		t.Fatalf("validateRunID rejected default: %v", err)
	}
	b, err := defaultNewRunID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("expected two calls to differ: both %q", a)
	}
}
