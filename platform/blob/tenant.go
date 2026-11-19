package blob

import (
	"strings"
	"unicode"

	"github.com/epzero/basamento/core"
)

// validateTenantSegment rejects tenant ids that are unsafe as one bucket path
// segment. Until EP0-52 adds shared rules in core, blob enforces this locally.
func validateTenantSegment(tid string) error {
	if tid == "" {
		return core.NewError(core.CodeInvalidArgument, "tenant required", nil)
	}
	if len(tid) > 128 {
		return core.NewError(core.CodeInvalidArgument, "invalid tenant id", nil)
	}
	if tid == "." || tid == ".." {
		return core.NewError(core.CodeInvalidArgument, "invalid tenant id", nil)
	}
	if strings.ContainsAny(tid, "/\\") {
		return core.NewError(core.CodeInvalidArgument, "invalid tenant id", nil)
	}
	for _, r := range tid {
		if r == 0 || unicode.IsControl(r) {
			return core.NewError(core.CodeInvalidArgument, "invalid tenant id", nil)
		}
	}
	return nil
}
