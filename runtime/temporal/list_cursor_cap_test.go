package temporal

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func maxLegitimateListCursor(t *testing.T) string {
	t.Helper()
	tenant := core.TenantID(strings.Repeat("a", 128))
	q, err := listQuery(tenant, "", "")
	if err != nil {
		t.Fatal(err)
	}
	pageToken := make([]byte, 2048)
	for i := range pageToken {
		pageToken[i] = byte(i % 256)
	}
	tok, err := encodeListCursor(tenant, q, pageToken)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func paddedListCursor(t *testing.T, tenant core.TenantID, q string, pageToken []byte, pad int) string {
	t.Helper()
	base, err := encodeListCursor(tenant, q, pageToken)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(base)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if pad > 0 {
		payload["_pad"] = strings.Repeat("x", pad)
	}
	inner, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(inner)
}

func TestListCursorMaxLegitimateFitsUnderCap(t *testing.T) {
	tok := maxLegitimateListCursor(t)
	if len(tok) >= maxListCursorBytes {
		t.Fatalf("legitimate cursor len %d >= cap %d", len(tok), maxListCursorBytes)
	}
}

func TestListCursorLengthCap(t *testing.T) {
	tenant := core.TenantID("acme")
	q, err := listQuery(tenant, "", "")
	if err != nil {
		t.Fatal(err)
	}
	wantPage := []byte{1, 2, 3, 4}

	var atCap string
	for pad := 0; ; pad++ {
		outer := paddedListCursor(t, tenant, q, wantPage, pad)
		switch {
		case len(outer) == maxListCursorBytes:
			atCap = outer
		case len(outer) > maxListCursorBytes:
			t.Fatalf("reached pad %d len %d without hitting cap %d exactly", pad, len(outer), maxListCursorBytes)
		}
		if atCap != "" {
			break
		}
	}
	if atCap == "" {
		t.Fatal("no padding length yields outer encoding exactly at cap")
	}
	got, err := parseListCursor(atCap, tenant, q)
	if err != nil {
		t.Fatalf("at cap: err %v want success", err)
	}
	if string(got) != string(wantPage) {
		t.Fatalf("at cap page %v want %v", got, wantPage)
	}

	var overCap string
	for pad := 0; ; pad++ {
		outer := paddedListCursor(t, tenant, q, wantPage, pad)
		if len(outer) > maxListCursorBytes {
			overCap = outer
			break
		}
	}
	if overCap == "" {
		t.Fatal("no padding yields length above cap")
	}
	_, err = parseListCursor(overCap, tenant, q)
	if err == nil {
		t.Fatal("over-cap should fail")
	}
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Code != core.CodeInvalidArgument || pe.Message != "temporal: unusable paging cursor" {
		t.Fatalf("over-cap err %v", err)
	}
}
