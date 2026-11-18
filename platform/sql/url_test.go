package sql

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestParseStoreURLRejectsUnsupported(t *testing.T) {
	cases := []string{
		"mysql://kit@db.example.test/kit",
		"file:///{data}/kit.db",
		"sqlite://",
		"postgres://kit@db.example.test",
		"kit.db",
	}
	for _, raw := range cases {
		if _, err := parseStoreURL(raw); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("parseStoreURL(%q) = %v, want invalid_argument", raw, err)
		}
	}
}

func TestRedactURLStripsPassword(t *testing.T) {
	raw := "postgres://kit:s3cret-pw@127.0.0.1:9/kit"
	got := redactURL(raw)
	if stringsContains(got, "s3cret-pw") {
		t.Fatalf("redactURL leaked password: %s", got)
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
