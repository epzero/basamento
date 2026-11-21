package temporal

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"

	"github.com/epzero/basamento/core"
)

const listCursorVersion = 1

type listCursorPayload struct {
	V int    `json:"v"`
	T string `json:"t"`
	Q string `json:"q"`
	P string `json:"p"`
}

func queryFingerprint(q string) string {
	sum := sha256.Sum256([]byte(q))
	return hex.EncodeToString(sum[:])
}

func encodeListCursor(tenant core.TenantID, query string, pageToken []byte) (string, error) {
	if len(pageToken) == 0 {
		return "", nil
	}
	p := base64.StdEncoding.EncodeToString(pageToken)
	raw, err := json.Marshal(listCursorPayload{
		V: listCursorVersion,
		T: string(tenant),
		Q: queryFingerprint(query),
		P: p,
	})
	if err != nil {
		return "", core.NewError(core.CodeInternal, "temporal: encode list cursor", map[string]any{"cause": err.Error()})
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func parseListCursor(cursor string, tenant core.TenantID, query string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: unusable paging cursor", nil)
	}
	var payload listCursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: unusable paging cursor", nil)
	}
	if payload.V != listCursorVersion {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: unusable paging cursor", nil)
	}
	if payload.T != string(tenant) {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: unusable paging cursor", nil)
	}
	if payload.Q != queryFingerprint(query) {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: unusable paging cursor", nil)
	}
	if payload.P == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: unusable paging cursor", nil)
	}
	pageToken, err := base64.StdEncoding.DecodeString(payload.P)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: unusable paging cursor", nil)
	}
	return pageToken, nil
}
