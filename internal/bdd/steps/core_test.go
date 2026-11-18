package steps

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestParseTenantLiteralFromTable(t *testing.T) {
	// Each literal is what the Gherkin table layer delivers to the step, not
	// what the feature file shows: that layer consumes one backslash, so the
	// feature's row "a\\b" arrives here as `"a\b"`.
	tests := []struct {
		name    string
		literal string
		wantID  core.TenantID
		wantOK  bool
		wantErr bool
	}{
		{name: "absent", literal: "absent"},
		{name: "backslash", literal: `"a\b"`, wantID: `a\b`, wantOK: true},
		{name: "unicode escape", literal: `"a\u0000b"`, wantID: "a\x00b", wantOK: true},
		{name: "byte escape", literal: `"\xFF\xFE"`, wantID: "\xff\xfe", wantOK: true},
		{name: "quote escape", literal: `"a\"b"`, wantID: `a"b`, wantOK: true},
		{name: "undocumented tab stays literal", literal: `"a\tb"`, wantID: `a\tb`, wantOK: true},
		{name: "undocumented U stays literal", literal: `"\U0001F600"`, wantID: `\U0001F600`, wantOK: true},
		{name: "undocumented octal stays literal", literal: `"\101"`, wantID: `\101`, wantOK: true},
		{name: "not a literal", literal: "acme", wantErr: true},
		{name: "broken unicode escape", literal: `"a\uZZZZ"`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok, err := parseTenantLiteral(tt.literal)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTenantLiteral(%q) = (%q, %v), want an error", tt.literal, id, ok)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTenantLiteral(%q): %v", tt.literal, err)
			}
			if id != tt.wantID || ok != tt.wantOK {
				t.Fatalf("parseTenantLiteral(%q) = (%q, %v), want (%q, %v)", tt.literal, id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}
