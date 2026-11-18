package expr

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/interpreter"
)

// Program is a compiled expression safe for concurrent evaluation.
type Program struct {
	env        *Env
	src        string
	prog       cel.Program
	resultType string
}

// Compile type-checks an expression and optional expected result schema.
func (e *Env) Compile(src string, result json.RawMessage) (*Program, error) {
	ast, iss := e.celEnv.Compile(src)
	if iss != nil && iss.Err() != nil {
		return nil, formatCompileIssues(src, iss)
	}
	if ast == nil {
		return nil, compileErr(src, "failed to compile")
	}
	var expected *schemaType
	if len(result) > 0 {
		var err error
		expected, err = parseSchema(result, "result")
		if err != nil {
			return nil, compileErr(src, err.Error())
		}
	}
	outType := ast.OutputType()
	if expected != nil {
		if err := assignableResult(expected, outType); err != nil {
			return nil, compileErr(src, err.Error())
		}
	}
	opts := []cel.ProgramOption{
		cel.CostTracking(nil),
		cel.CostLimit(e.costLimit),
		cel.CustomDecoratorV2(sortedMapDecorator(e.adapter)),
	}
	prg, err := e.celEnv.Program(ast, opts...)
	if err != nil {
		return nil, compileErr(src, err.Error())
	}
	return &Program{
		env:        e,
		src:        src,
		prog:       prg,
		resultType: formatCELType(outType),
	}, nil
}

// ResultType returns the checked result type as a human-readable CEL type string.
func (p *Program) ResultType() string { return p.resultType }

// Eval evaluates the program with JSON variable bindings.
func (p *Program) Eval(vars map[string]any) (any, error) {
	activation, err := p.env.bindVars(vars)
	if err != nil {
		return nil, err
	}
	out, _, err := p.prog.Eval(activation)
	if err != nil {
		return nil, p.mapEvalError(err)
	}
	j, jerr := celValToJSON(out)
	if jerr != nil {
		if isJSONDepthErr(jerr) {
			return nil, evalErr(p.src, "evaluation failed")
		}
		cause := jerr.Error()
		if c := mapCELCause(cause); c != "" {
			cause = c
		}
		return nil, evalErr(p.src, cause)
	}
	return j, nil
}

var (
	undefFieldRe = regexp.MustCompile(`undefined field '([^']+)'`)
	undefRefRe   = regexp.MustCompile(`undeclared reference to '([^']+)'`)
)

func formatCompileIssues(src string, iss *cel.Issues) error {
	var parts []string
	for _, e := range iss.Errors() {
		detail := e.Message
		if m := undefFieldRe.FindStringSubmatch(detail); len(m) == 2 {
			detail = fmt.Sprintf(`undeclared property "%s"`, m[1])
		} else if m := undefRefRe.FindStringSubmatch(detail); len(m) == 2 {
			name := m[1]
			if idx := strings.LastIndex(name, "."); idx >= 0 {
				name = name[idx+1:]
			}
			detail = fmt.Sprintf(`undeclared reference "%s"`, name)
		}
		loc := e.Location
		if loc != nil && loc.Line() != 0 {
			parts = append(parts, fmt.Sprintf("line %d column %d: %s", loc.Line(), loc.Column(), detail))
		} else {
			parts = append(parts, detail)
		}
	}
	msg := fmt.Sprintf(`expression %q: %s`, src, strings.Join(parts, "; "))
	return exprErr(msg, map[string]any{"expression": src})
}

func (p *Program) mapEvalError(err error) error {
	var cancel interpreter.EvalCancelledError
	if errors.As(err, &cancel) && cancel.Cause == interpreter.CostLimitExceeded {
		return evalErr(p.src, fmt.Sprintf("cost limit %d exceeded", p.env.costLimit))
	}
	cause := mapCELCause(err.Error())
	if cause == "" {
		cause = "evaluation failed"
	}
	return evalErr(p.src, cause)
}

func mapCELCause(msg string) string {
	switch {
	case strings.Contains(msg, "division by zero"):
		return "division by zero"
	case strings.Contains(strings.ToLower(msg), "overflow"):
		return "overflow"
	case strings.Contains(msg, "index out of range"), strings.Contains(msg, "out of bounds"):
		return "out of bounds"
	case strings.Contains(msg, "no such key"), strings.Contains(msg, "does not exist"):
		return "no such key"
	case strings.Contains(msg, "NaN"), strings.Contains(msg, "not a valid JSON"), strings.Contains(msg, "no JSON form"):
		return "no JSON form"
	default:
		return ""
	}
}

func (e *Env) bindVars(vars map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(vars))
	w := newJSONWalk()
	for name, val := range vars {
		st, ok := e.schemas[name]
		if !ok {
			return nil, bindErr(name, "undeclared variable")
		}
		bound, err := bindJSONValue(st, val, e, w)
		if err != nil {
			if isJSONDepthErr(err) {
				return nil, bindErr(name, "value too deep")
			}
			return nil, bindErr(name, err.Error())
		}
		out[name] = bound
	}
	return out, nil
}

func bindJSONValue(st *schemaType, val any, e *Env, w *jsonWalk) (any, error) {
	if val == nil {
		return nil, nil
	}
	if st.celType == cel.TimestampType {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("wrong type")
		}
		ts, err := types.ParseTimestamp(s)
		if err != nil {
			return nil, fmt.Errorf("wrong type")
		}
		return ts, nil
	}
	switch st.celType.Kind() {
	case cel.IntKind:
		return bindInt(val)
	case cel.BoolKind:
		b, ok := val.(bool)
		if !ok {
			return nil, fmt.Errorf("wrong type")
		}
		return b, nil
	case cel.StringKind:
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("wrong type")
		}
		return s, nil
	case cel.ListKind:
		arr, ok := val.([]any)
		if !ok {
			return nil, fmt.Errorf("wrong type")
		}
		ptr := addrOf(arr)
		if err := w.down(ptr); err != nil {
			return nil, err
		}
		defer w.up(ptr)
		elem := st.celType.Parameters()[0]
		out := make([]any, len(arr))
		for i, item := range arr {
			sub := &schemaType{celType: elem}
			v, err := bindJSONValue(sub, item, e, w)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case cel.MapKind:
		m, ok := val.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("wrong type")
		}
		ptr := addrOf(m)
		if err := w.down(ptr); err != nil {
			return nil, err
		}
		defer w.up(ptr)
		valType := st.celType.Parameters()[1]
		out := make(map[string]any, len(m))
		for k, v := range m {
			sub := &schemaType{celType: valType}
			bv, err := bindJSONValue(sub, v, e, w)
			if err != nil {
				return nil, err
			}
			out[k] = bv
		}
		return out, nil
	case cel.StructKind:
		if st.typeName != "" {
			if obj := e.objects[st.typeName]; obj != nil {
				m, ok := val.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("wrong type")
				}
				ptr := addrOf(m)
				if err := w.down(ptr); err != nil {
					return nil, err
				}
				defer w.up(ptr)
				return obj.Adapt(e.adapter, m), nil
			}
		}
		return val, nil
	default:
		return val, nil
	}
}

func bindInt(val any) (int64, error) {
	switch v := val.(type) {
	case json.Number:
		if _, err := v.Int64(); err != nil {
			return 0, fmt.Errorf("wrong type")
		}
		i, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("wrong type")
		}
		return i, nil
	case float64:
		i, ok := float64IntegralInt64(v)
		if !ok {
			return 0, fmt.Errorf("wrong type")
		}
		return i, nil
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	default:
		return 0, fmt.Errorf("wrong type")
	}
}
