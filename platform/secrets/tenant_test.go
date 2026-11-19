package secrets

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestValidateTenantREQCORE005(t *testing.T) {
	t.Parallel()
	valid := []string{
		"acme",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"acme-東京",
		"a..b",
		"a b",
	}
	for _, id := range valid {
		if err := ValidateTenant(id); err != nil {
			t.Fatalf("%q valid: %v", id, err)
		}
	}
	invalid := []string{
		"",
		".",
		"..",
		"a/.part",
		"a\\b",
		"\xff\xfe",
		"a\u0000b",
		"a\u000ab",
		"a\u001fb",
		"a\u007fb",
		" acme",
		"acme ",
		"\u00a0acme",
		strings.Repeat("a", 129),
		"ééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééé",
	}
	for _, id := range invalid {
		err := ValidateTenant(id)
		if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("%q: err=%v", id, err)
		}
	}
	longUTF8 := "ééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééé"
	if len(longUTF8) != 130 {
		t.Fatalf("long UTF-8 case len=%d", len(longUTF8))
	}
	if err := ValidateTenant(longUTF8); err == nil {
		t.Fatal("130-byte UTF-8 tenant accepted")
	}
}
