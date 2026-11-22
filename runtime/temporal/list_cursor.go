package temporal

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"

	"github.com/epzero/basamento/core"
)

const listCursorVersion = 1

// maxListCursorBytes is the longest accepted opaque list cursor before decoding.
// The largest encodeListCursor output uses a 128-byte tenant id (REQ-CORE-005),
// a 64-character hex query fingerprint, and a page token of 2048 bytes (standard
// base64 on P is 2732 characters). The JSON payload is about 2957 bytes and the
// outer base64url encoding is about 3944 bytes, well under this cap.
const maxListCursorBytes = 4096

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
	if len(cursor) > maxListCursorBytes {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: unusable paging cursor", nil)
	}
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
