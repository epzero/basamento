package local

import (
	"encoding/base64"
	"encoding/json"

	"github.com/epzero/basamento/core"
)

const listCursorVersion = 1

type listCursorPayload struct {
	V      int    `json:"v"`
	Tenant string `json:"tenant"`
	Pos    int    `json:"pos"`
}

// encodeListCursor returns the opaque paging token naming position pos in the
// tenant's start-ordered run list. The token is not signed: a forged position
// within the tenant's own range only lists that tenant's runs.
func encodeListCursor(tenant core.TenantID, pos int) (string, error) {
	raw, err := json.Marshal(listCursorPayload{V: listCursorVersion, Tenant: string(tenant), Pos: pos})
	if err != nil {
		return "", core.NewError(core.CodeInternal, "local: encode list cursor", map[string]any{"cause": err.Error()})
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// parseListCursor decodes a paging token for tenant and checks that pos names a
// position in a list of runCount runs. Validation runs before any run is read.
func parseListCursor(cursor string, tenant core.TenantID, runCount int) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, invalidErr("local: invalid list cursor", nil)
	}
	var payload listCursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, invalidErr("local: invalid list cursor", nil)
	}
	// Canonical equality is the sole format gate: only encodeListCursor's JSON
	// (fixed version, fields, and encoding) is accepted, so wrong versions,
	// unknown fields, extra JSON, and non-canonical base64url are rejected.
	canonical, err := encodeListCursor(core.TenantID(payload.Tenant), payload.Pos)
	if err != nil || canonical != cursor {
		return 0, invalidErr("local: invalid list cursor", nil)
	}
	if payload.Tenant != string(tenant) {
		return 0, invalidErr("local: invalid list cursor", nil)
	}
	if payload.Pos < 0 || payload.Pos >= runCount {
		return 0, invalidErr("local: invalid list cursor", nil)
	}
	return payload.Pos, nil
}

// tenantRunSnapshot returns the tenant's runs in start order (a copy of pointers).
func tenantRunSnapshot(order []*run, tenant core.TenantID) []*run {
	out := make([]*run, 0)
	for _, candidate := range order {
		if candidate.tenant == tenant {
			out = append(out, candidate)
		}
	}
	return out
}