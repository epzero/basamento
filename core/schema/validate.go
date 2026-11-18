package schema

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	js "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

func validationFailure(err error) error {
	var verr *js.ValidationError
	if !errors.As(err, &verr) {
		return invalidArgf("validate instance: %v", err)
	}
	violations := normalizeViolations(flattenInstanceErrors(verr))
	return invalidArg("instance does not match schema", map[string]any{"violations": violations})
}

func flattenInstanceErrors(err *js.ValidationError) []Violation {
	var out []Violation
	var walk func(e *js.ValidationError)
	walk = func(e *js.ValidationError) {
		if e == nil {
			return
		}
		switch k := e.ErrorKind.(type) {
		case *kind.Group:
			for _, c := range e.Causes {
				walk(c)
			}
			return
		case *kind.Reference:
			for _, c := range e.Causes {
				walk(c)
			}
			return
		case *kind.Schema:
			for _, c := range e.Causes {
				walk(c)
			}
			return
		case *kind.Required:
			for _, missing := range k.Missing {
				out = append(out, Violation{
					Location: ptrJoinTokens(append(e.InstanceLocation, missing)),
					Keyword:  "required",
					Message:  fmt.Sprintf("missing property %q", missing),
				})
			}
			return
		case *kind.AdditionalProperties:
			for _, prop := range k.Properties {
				out = append(out, Violation{
					Location: ptrJoinTokens(append(e.InstanceLocation, prop)),
					Keyword:  "additionalProperties",
					Message:  fmt.Sprintf("additional property %q not allowed", prop),
				})
			}
			return
		default:
			if len(e.Causes) > 0 {
				for _, c := range e.Causes {
					walk(c)
				}
				return
			}
			kw := ""
			if e.ErrorKind != nil {
				p := e.ErrorKind.KeywordPath()
				if len(p) > 0 {
					kw = p[len(p)-1]
				}
			}
			if kw == "" {
				return
			}
			out = append(out, Violation{
				Location: instancePtr(e.InstanceLocation),
				Keyword:  kw,
				Message:  instanceViolationMessage(kw),
			})
		}
	}
	walk(err)
	return out
}

func normalizeViolations(vs []Violation) []Violation {
	slices.SortFunc(vs, func(a, b Violation) int {
		if c := cmp.Compare(a.Location, b.Location); c != 0 {
			return c
		}
		return cmp.Compare(a.Keyword, b.Keyword)
	})
	return vs
}

func instancePtr(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	var b bytes.Buffer
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(escapeJSONPtrToken(t))
	}
	return b.String()
}

func ptrJoinTokens(tokens []string) string {
	return instancePtr(tokens)
}

func collectSchemaViolations(docURL string, err *js.ValidationError) []SchemaViolation {
	var out []SchemaViolation
	var walk func(e *js.ValidationError)
	walk = func(e *js.ValidationError) {
		if e == nil {
			return
		}
		switch e.ErrorKind.(type) {
		case *kind.Group, *kind.Reference, *kind.Schema:
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		if e.ErrorKind == nil {
			return
		}
		path := e.ErrorKind.KeywordPath()
		if len(path) == 0 {
			return
		}
		loc := instancePtr(e.InstanceLocation)
		if loc == "" {
			loc = keywordPathToSchemaLoc(path)
		}
		kw := path[len(path)-1]
		out = append(out, SchemaViolation{
			SchemaLocation: loc,
			Keyword:        kw,
			Message:        schemaViolationMessage(kw),
		})
		_ = docURL
	}
	walk(err)
	slices.SortFunc(out, func(a, b SchemaViolation) int {
		if c := cmp.Compare(a.SchemaLocation, b.SchemaLocation); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Keyword, b.Keyword); c != 0 {
			return c
		}
		return cmp.Compare(a.Message, b.Message)
	})
	return out
}

func instanceViolationMessage(keyword string) string {
	return fmt.Sprintf("instance does not satisfy %q", keyword)
}

func schemaViolationMessage(keyword string) string {
	return fmt.Sprintf("schema does not satisfy %q", keyword)
}

func keywordPathToSchemaLoc(path []string) string {
	var b strings.Builder
	for _, p := range path {
		b.WriteByte('/')
		b.WriteString(escapeJSONPtrToken(p))
	}
	return b.String()
}

func compilationFailure(root string, err error) error {
	var sve *js.SchemaValidationError
	if errors.As(err, &sve) {
		var verr *js.ValidationError
		if errors.As(sve.Err, &verr) {
			violations := collectSchemaViolations(root, verr)
			if len(violations) > 0 {
				return invalidArg("invalid schema", map[string]any{"violations": violations})
			}
		}
	}

	var load *js.LoadURLError
	if errors.As(err, &load) {
		ref := load.URL
		if strings.Contains(ref, "://") {
			return invalidArg("schema reference not in set", map[string]any{"reference": ref})
		}
		return invalidArg("schema reference not in set", map[string]any{"reference": ref})
	}

	// Unresolved relative ref.
	msg := err.Error()
	if strings.Contains(msg, "failing loading ") {
		if u := extractQuotedURL(msg); u != "" && !strings.HasPrefix(u, "https://json-schema.org/") {
			if strings.Contains(u, "://") {
				return invalidArg("schema reference not in set", map[string]any{"reference": u})
			}
			return invalidArg("schema reference not in set", map[string]any{"reference": u})
		}
	}
	return invalidArgf("compile schema: %v", err)
}

func extractQuotedURL(msg string) string {
	i := strings.Index(msg, `"`)
	if i < 0 {
		return ""
	}
	rest := msg[i+1:]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
