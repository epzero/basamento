package local

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func listCursorPositionForTests(tenant core.TenantID, pos int) string {
	tok, err := encodeListCursor(tenant, pos)
	if err != nil {
		panic(fmt.Sprintf("list cursor: %v", err))
	}
	return tok
}

func TestEncodeDecodeListCursor(t *testing.T) {
	tok, err := encodeListCursor("acme", 2)
	if err != nil {
		t.Fatal(err)
	}
	pos, err := parseListCursor(tok, "acme", 5)
	if err != nil || pos != 2 {
		t.Fatalf("parse = %d, %v, want 2", pos, err)
	}
}

func TestListCursorRejectsNonCanonical(t *testing.T) {
	raw, _ := json.Marshal(listCursorPayload{V: 1, Tenant: "acme", Pos: 0})
	// Pad base64 breaks canonical form (RawURLEncoding has no padding).
	padded := base64.URLEncoding.EncodeToString(raw)
	if _, err := parseListCursor(padded, "acme", 3); codeOf(err) != "invalid_argument" {
		t.Fatalf("non-canonical = %v, want invalid_argument", err)
	}
}

func TestListCursorRejectsPositionAtEnd(t *testing.T) {
	tok := listCursorPositionForTests("acme", 2)
	if _, err := parseListCursor(tok, "acme", 2); codeOf(err) != "invalid_argument" {
		t.Fatalf("pos at end = %v, want invalid_argument", err)
	}
}

func TestListCursorRejectsWrongTenant(t *testing.T) {
	tok := listCursorPositionForTests("acme", 0)
	_, err := parseListCursor(tok, "globex", 1)
	if codeOf(err) != "invalid_argument" {
		t.Fatalf("wrong tenant = %v, want invalid_argument", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "acme") {
		t.Fatalf("error echoes foreign tenant cursor: %q", msg)
	}
}

func TestListCursorRejectsUndecodable(t *testing.T) {
	if _, err := parseListCursor("not-a-token", "acme", 1); codeOf(err) != "invalid_argument" {
		t.Fatalf("undecodable = %v, want invalid_argument", err)
	}
}

func TestListCursorRejectsUnknownJSONField(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"v": 1, "tenant": "acme", "pos": 0, "extra": 1})
	tok := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := parseListCursor(tok, "acme", 1); codeOf(err) != "invalid_argument" {
		t.Fatalf("unknown field = %v, want invalid_argument", err)
	}
}

func TestListCursorRejectsNegativePosition(t *testing.T) {
	tok := listCursorPositionForTests("acme", -1)
	if _, err := parseListCursor(tok, "acme", 3); codeOf(err) != "invalid_argument" {
		t.Fatalf("negative pos = %v, want invalid_argument", err)
	}
}
