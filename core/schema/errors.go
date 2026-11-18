package schema

import (
	"errors"
	"fmt"
	"strings"

	"github.com/epzero/basamento/core"
)

// Violation is one schema or instance constraint failure.
type Violation struct {
	Location string `json:"location"`
	Keyword  string `json:"keyword"`
	Message  string `json:"message"`
}

// SchemaViolation is a metaschema failure while compiling a schema.
type SchemaViolation struct {
	SchemaLocation string `json:"schema_location"`
	Keyword        string `json:"keyword"`
	Message        string `json:"message"`
}

func invalidArg(msg string, details map[string]any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, msg, details)
}

func invalidArgf(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...), nil)
}

func escapeJSONPtrToken(tok string) string {
	tok = strings.ReplaceAll(tok, "~", "~0")
	return strings.ReplaceAll(tok, "/", "~1")
}

// ErrLocation returns the instance or schema location from a platform error, if any.
func ErrLocation(err error) string {
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Details == nil {
		return ""
	}
	if loc, ok := pe.Details["location"].(string); ok {
		return loc
	}
	return ""
}

// Violations returns instance validation violations from err.
func Violations(err error) []Violation {
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Details == nil {
		return nil
	}
	raw, ok := pe.Details["violations"].([]Violation)
	if ok {
		return raw
	}
	sl, ok := pe.Details["violations"].([]any)
	if !ok {
		return nil
	}
	var out []Violation
	for _, item := range sl {
		switch v := item.(type) {
		case Violation:
			out = append(out, v)
		case map[string]any:
			out = append(out, Violation{
				Location: fmt.Sprint(v["location"]),
				Keyword:  fmt.Sprint(v["keyword"]),
				Message:  fmt.Sprint(v["message"]),
			})
		}
	}
	return out
}

// ReferenceFromErr returns an external reference named in a compilation error.
func ReferenceFromErr(err error) string {
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Details == nil {
		return ""
	}
	if ref, ok := pe.Details["reference"].(string); ok {
		return ref
	}
	return ""
}

// SchemaViolations returns compilation violations from err.
func SchemaViolations(err error) []SchemaViolation {
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Details == nil {
		return nil
	}
	if vs, ok := pe.Details["violations"].([]SchemaViolation); ok {
		return vs
	}
	return nil
}
