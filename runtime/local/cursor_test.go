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

func nonCanonicalListCursor(t *testing.T, rawJSON string) string {
	t.Helper()
	tok := base64.RawURLEncoding.EncodeToString([]byte(rawJSON))
	var probe listCursorPayload
	if err := json.Unmarshal([]byte(rawJSON), &probe); err != nil {
		t.Fatalf("probe decode %q: %v", rawJSON, err)
	}
	canonical, err := encodeListCursor(core.TenantID(probe.Tenant), probe.Pos)
	if err != nil {
		t.Fatal(err)
	}
	if canonical == tok {
		t.Fatalf("test token is already canonical: %q", tok)
	}
	return tok
}

func TestListCursorRejectsNonCanonicalJSON(t *testing.T) {
	cases := []string{
		`{"pos":0,"tenant":"acme","v":1}`,
		`{"v":1, "tenant": "acme", "pos": 0}`,
		`{"v":1,"tenant":"acme","pos":0,"extra":1}`,
	}
	for _, raw := range cases {
		tok := nonCanonicalListCursor(t, raw)
		if _, err := parseListCursor(tok, "acme", 3); codeOf(err) != "invalid_argument" {
			t.Fatalf("token from %q = %v, want invalid_argument", raw, err)
		}
	}
}

func TestListCursorRejectsNonCanonicalBase64(t *testing.T) {
	raw, _ := json.Marshal(listCursorPayload{V: 1, Tenant: "acme", Pos: 0})
	// Standard base64 with padding decodes via RawURLEncoding but is not the canonical token.
	padded := base64.StdEncoding.EncodeToString(raw)
	if _, err := parseListCursor(padded, "acme", 3); codeOf(err) != "invalid_argument" {
		t.Fatalf("non-canonical base64 = %v, want invalid_argument", err)
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
