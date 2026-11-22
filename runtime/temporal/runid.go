package temporal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/epzero/basamento/core"
)

func defaultNewRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "run_" + hex.EncodeToString(b[:]), nil
}

func deriveRunID(tenant core.TenantID, key string) string {
	sum := sha256.Sum256([]byte(string(tenant) + "\x00" + key))
	return "idem_" + hex.EncodeToString(sum[:])
}

func validateRunID(runID string) error {
	if runID == "" {
		return core.NewError(core.CodeInvalidArgument, "temporal: empty run id", nil)
	}
	if len(runID) > 255 || !utf8.ValidString(runID) {
		return core.NewError(core.CodeInvalidArgument, "temporal: invalid run id", nil)
	}
	for _, r := range runID {
		if unicode.IsControl(r) {
			return core.NewError(core.CodeInvalidArgument, "temporal: invalid run id", nil)
		}
	}
	if strings.ContainsRune(runID, '/') {
		return core.NewError(core.CodeInvalidArgument, "temporal: invalid run id", nil)
	}
	return nil
}

func validateFlowFilter(flow string) error {
	if flow == "" {
		return nil
	}
	if len(flow) > 255 || !utf8.ValidString(flow) {
		return core.NewError(core.CodeInvalidArgument, "temporal: invalid flow filter", nil)
	}
	for _, r := range flow {
		if unsafeVisibilityRune(r) {
			return core.NewError(core.CodeInvalidArgument, "temporal: invalid flow filter", nil)
		}
	}
	return nil
}

// unsafeVisibilityRune reports whether r cannot appear inside a double-quoted
// visibility literal (including backslash, which breaks escape sequences).
func unsafeVisibilityRune(r rune) bool {
	if r == '\\' {
		return true
	}
	if r == '"' || r == '\'' || r == '`' {
		return true
	}
	// ASCII quotes are handled above; Quotation_Mark also matches U+0027/U+0022.
	if r > 127 && unicode.Is(unicode.Quotation_Mark, r) {
		return true
	}
	return unicode.IsControl(r)
}
