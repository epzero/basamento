package expr

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/itchyny/gojq"
)

// MappingOption configures a mapping.
type MappingOption func(*Mapping) error

// WithStepLimit sets the deterministic step limit (default 1_000_000).
func WithStepLimit(limit uint64) MappingOption {
	return func(m *Mapping) error {
		m.stepLimit = limit
		return nil
	}
}

// Mapping is a compiled gojq program.
type Mapping struct {
	src       string
	code      *gojq.Code
	vars      []string
	stepLimit uint64
}

// CompileMapping parses and validates a jq mapping.
func CompileMapping(src string, varNames []string, opts ...MappingOption) (*Mapping, error) {
	for _, v := range varNames {
		if v == "$ENV" {
			return nil, mappingCompileErr(src, `undeclared reference "$ENV"`)
		}
	}
	query, err := gojq.Parse(src)
	if err != nil {
		return nil, formatMappingParseErr(src, err)
	}
	if name, blocked := scanBanned(query); blocked {
		return nil, mappingCompileErr(src, fmt.Sprintf(`undeclared reference "%s"`, name))
	}
	code, err := gojq.Compile(query, gojq.WithVariables(varNames))
	if err != nil {
		return nil, mappingCompileErr(src, err.Error())
	}
	m := &Mapping{
		src:       src,
		code:      code,
		vars:      append([]string(nil), varNames...),
		stepLimit: defaultStepLimit,
	}
	for _, o := range opts {
		if err := o(m); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Eval runs the mapping on input with named variables bound in declaration order.
func (m *Mapping) Eval(input any, vars map[string]any) (any, error) {
	values := make([]any, len(m.vars))
	for i, name := range m.vars {
		values[i] = vars[name]
	}
	input = normalizeMappingInput(input)
	ctx := newStepContext(m.stepLimit)
	iter := m.code.RunWithContext(ctx, input, values...)
	var first any
	var count int
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			if ctx.limitExceeded() {
				return nil, mappingEvalErr(fmt.Sprintf("step limit %d exceeded", m.stepLimit))
			}
			cause := mapGoJQCause(err.Error())
			if cause == "" {
				cause = "mapping evaluation failed"
			}
			return nil, mappingEvalErr(cause)
		}
		count++
		if count == 1 {
			first = normalizeMappingOutput(v)
			continue
		}
		return nil, mappingEvalErr("mapping produced more than one value")
	}
	if count == 0 {
		return nil, mappingEvalErr("mapping produced no value")
	}
	if count > 1 {
		return nil, mappingEvalErr("mapping produced more than one value")
	}
	return first, nil
}

func formatMappingParseErr(src string, err error) error {
	var pe *gojq.ParseError
	if errors.As(err, &pe) {
		return mappingCompileErr(src, fmt.Sprintf("parse error at offset %d", pe.Offset))
	}
	return mappingCompileErr(src, err.Error())
}

func normalizeMappingInput(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return normalizeMappingInputMap(t)
	case nil:
		return nil
	default:
		return normalizeMappingInputValue(v)
	}
}

func normalizeMappingInputMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, val := range m {
		out[k] = normalizeMappingInputValue(val)
	}
	return out
}

func mapGoJQCause(msg string) string {
	switch {
	case strings.Contains(msg, "cannot be sorted"):
		return "mapping produced more than one value"
	case strings.Contains(msg, "division by zero"):
		return "division by zero"
	case strings.Contains(strings.ToLower(msg), "overflow"):
		return "overflow"
	case strings.Contains(msg, "index out of range"), strings.Contains(msg, "out of bounds"):
		return "out of bounds"
	case strings.Contains(msg, "no such key"), strings.Contains(msg, "does not exist"):
		return "no such key"
	default:
		return ""
	}
}

func normalizeMappingInputValue(v any) any {
	switch t := v.(type) {
	case int64:
		return int(t)
	case int:
		return t
	case json.Number:
		bi := new(big.Int)
		if _, ok := bi.SetString(string(t), 10); ok {
			if bi.IsInt64() {
				return int(bi.Int64())
			}
			return bi
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t
	case map[string]any:
		return normalizeMappingInputMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeMappingInputValue(e)
		}
		return out
	default:
		return v
	}
}

func normalizeMappingOutput(v any) any {
	switch t := v.(type) {
	case *big.Int:
		if t.IsInt64() {
			return t.Int64()
		}
		return json.Number(t.String())
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t
	case map[string]any:
		sorted := sortObjectMap(t)
		for k, v := range sorted {
			sorted[k] = normalizeMappingOutput(v)
		}
		return sorted
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeMappingOutput(e)
		}
		return out
	default:
		return v
	}
}

func scanBanned(query *gojq.Query) (string, bool) {
	if query == nil {
		return "", false
	}
	if len(query.Imports) > 0 {
		im := query.Imports[0]
		if im.ImportPath != "" {
			return "import", true
		}
		return "include", true
	}
	for _, fd := range query.FuncDefs {
		if n, ok := scanBannedQuery(fd.Body); ok {
			return n, true
		}
	}
	return scanBannedQuery(query)
}

var bannedNames = map[string]bool{
	"now": true, "localtime": true, "strflocaltime": true,
	"env": true, "$ENV": true,
	"input": true, "inputs": true, "input_filename": true,
	"debug": true, "stderr": true, "halt": true, "halt_error": true,
	"import": true, "include": true, "modulemeta": true,
}

func scanBannedQuery(q *gojq.Query) (string, bool) {
	if q == nil {
		return "", false
	}
	if q.Term != nil {
		if n, ok := scanBannedTerm(q.Term); ok {
			return n, true
		}
	}
	if q.Left != nil {
		if n, ok := scanBannedQuery(q.Left); ok {
			return n, true
		}
	}
	if q.Right != nil {
		if n, ok := scanBannedQuery(q.Right); ok {
			return n, true
		}
	}
	return "", false
}

func scanBannedTerm(t *gojq.Term) (string, bool) {
	if t == nil {
		return "", false
	}
	if t.Type == gojq.TermTypeFunc && t.Func != nil {
		name := t.Func.Name
		if bannedNames[name] {
			return strings.TrimPrefix(name, "$"), true
		}
		for _, arg := range t.Func.Args {
			if n, ok := scanBannedQuery(arg); ok {
				return n, true
			}
		}
	}
	if t.Query != nil {
		if n, ok := scanBannedQuery(t.Query); ok {
			return n, true
		}
	}
	for _, s := range t.SuffixList {
		if s.Index != nil && s.Index.Start != nil {
			if n, ok := scanBannedIndex(s.Index.Start); ok {
				return n, true
			}
		}
	}
	return "", false
}

func scanBannedIndex(q *gojq.Query) (string, bool) {
	if q == nil || q.Term == nil {
		return "", false
	}
	if q.Term.Type == gojq.TermTypeFunc && q.Term.Func != nil {
		name := q.Term.Func.Name
		if bannedNames[name] {
			return strings.TrimPrefix(name, "$"), true
		}
	}
	return "", false
}
