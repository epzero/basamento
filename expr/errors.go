package expr

import (
	"fmt"
	"strings"

	"github.com/epzero/basamento/core"
)

func exprErr(msg string, details map[string]any) error {
	return core.NewError(core.CodeInvalidArgument, msg, details)
}

func compileErr(src, detail string) error {
	return exprErr(fmt.Sprintf("expression %q: %s", src, detail), map[string]any{"expression": src})
}

func evalErr(src, detail string) error {
	return exprErr(fmt.Sprintf("expression %q: %s", src, detail), map[string]any{"expression": src})
}

func bindErr(name, detail string) error {
	return exprErr(fmt.Sprintf("variable %q: %s", name, detail), map[string]any{"variable": name})
}

func mappingCompileErr(src, detail string) error {
	return exprErr(fmt.Sprintf("mapping %q: %s", src, detail), map[string]any{"mapping": src})
}

func mappingEvalErr(detail string) error {
	return exprErr("mapping: "+detail, nil)
}

func containsAll(msg string, parts ...string) bool {
	for _, p := range parts {
		if p != "" && !strings.Contains(msg, p) {
			return false
		}
	}
	return true
}
