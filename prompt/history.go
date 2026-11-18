package prompt

import (
	"regexp"

	"github.com/epzero/basamento/core"
)

// History roles are short ASCII tokens so roles cannot inject delimiters into model-facing text.
var historyRolePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)

func validateHistory(entries []HistoryEntry) error {
	for i, h := range entries {
		if !historyRolePattern.MatchString(h.Role) {
			return core.NewError(core.CodeInvalidArgument, "invalid history role", map[string]any{"index": i})
		}
	}
	return nil
}