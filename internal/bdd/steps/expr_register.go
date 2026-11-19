package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/expr"
)

func registerExpr(sc *godog.ScenarioContext) {
	sc.Step(`^an expression environment declaring no variables$`, func(ctx context.Context) error {
		return exprNewEnv(ctx, nil, nil)
	})

	sc.Step(`^an expression environment declaring "([^"]*)" with schema:$`, func(ctx context.Context, name string, doc *godog.DocString) error {
		return exprDeclare(ctx, name, strings.TrimSpace(doc.Content))
	})

	sc.Step(`^an expression environment declaring "([^"]*)" with schema (.+)$`, func(ctx context.Context, name, tail string) error {
		tail = strings.TrimSpace(tail)
		if m := costLimitSuffixRe.FindStringSubmatch(tail); len(m) == 3 {
			limit, err := strconv.ParseUint(m[2], 10, 64)
			if err != nil {
				return err
			}
			return exprDeclareCost(ctx, name, strings.TrimSpace(m[1]), limit)
		}
		return exprDeclare(ctx, name, tail)
	})

	sc.Step(`^an expression environment declaring "([^"]*)" as an integer, "([^"]*)" as a list of integers and "([^"]*)" as a map of integers$`,
		func(ctx context.Context, n, items, m string) error {
			vars := []expr.Var{
				{Name: n, Schema: json.RawMessage(`{"type":"integer"}`)},
				{Name: items, Schema: json.RawMessage(`{"type":"array","items":{"type":"integer"}}`)},
				{Name: m, Schema: json.RawMessage(`{"type":"object","additionalProperties":{"type":"integer"}}`)},
			}
			return exprNewEnv(ctx, vars, nil)
		})

	sc.Step(`^an expression environment declaring "([^"]*)" with an optional string property "([^"]*)"$`,
		func(ctx context.Context, state, prop string) error {
			schema := fmt.Sprintf(`{"type":"object","properties":{"%s":{"type":"string"}}}`, prop)
			return exprDeclare(ctx, state, schema)
		})

	sc.Step(`^the expression "([^"]*)" is compiled$`, func(ctx context.Context, src string) error {
		return exprCompileImpl(ctx, src, "")
	})
	sc.Step(`^the expression "([^"]*)" is compiled expecting a result of schema (.+)$`,
		func(ctx context.Context, src, resultSchema string) error {
			return exprCompileImpl(ctx, src, strings.TrimSpace(resultSchema))
		})

	sc.Step(`^compilation succeeds with result type "([^"]*)"$`, func(ctx context.Context, want string) error {
		e := world(ctx).Expr
		if e.compileErr != nil {
			return fmt.Errorf("compilation failed: %w", e.compileErr)
		}
		if e.program == nil {
			return fmt.Errorf("no compiled program")
		}
		got := e.resultType
		if got != want {
			return fmt.Errorf("result type %q, want %q", got, want)
		}
		return nil
	})

	sc.Step(`^compilation fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		e := world(ctx).Expr
		var err error
		switch {
		case e.mapCompile != nil:
			err = e.mapCompile
		case e.compileErr != nil:
			err = e.compileErr
		case e.envErr != nil:
			err = e.envErr
		default:
			return fmt.Errorf("compilation succeeded, want %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s: %w", got, code, err)
		}
		return nil
	})

	sc.Step(`^the error names the expression and the line and column of the error$`, func(ctx context.Context) error {
		return exprMsgContains(ctx, "line", "column")
	})

	sc.Step(`^the error names the undeclared reference "([^"]*)"$`, func(ctx context.Context, name string) error {
		return exprMsgContains(ctx, name)
	})

	sc.Step(`^the error names the undeclared property "([^"]*)"$`, func(ctx context.Context, name string) error {
		return exprMsgContains(ctx, name)
	})

	sc.Step(`^the error names the expected type "([^"]*)" and the actual type "([^"]*)"$`,
		func(ctx context.Context, exp, act string) error {
			return exprMsgContains(ctx, exp, act)
		})

	sc.Step(`^the error names the reference "([^"]*)"$`, func(ctx context.Context, ref string) error {
		return exprMsgContains(ctx, ref)
	})

	sc.Step(`^the expression "([^"]*)" is evaluated$`, func(ctx context.Context, src string) error {
		return exprEval(ctx, src, 1, "", nil)
	})

	sc.Step(`^the expression "([^"]*)" is evaluated (\d+) times$`, func(ctx context.Context, src string, n int) error {
		return exprEval(ctx, src, n, "", nil)
	})

	sc.Step(`^the expression "([^"]*)" is evaluated with "([^"]*)" bound to (.+)$`,
		func(ctx context.Context, src, varName, raw string) error {
			val, err := parseJSONValue(strings.TrimSpace(raw))
			if err != nil {
				return err
			}
			world(ctx).Expr.bindings[varName] = val
			return exprEval(ctx, src, 1, "", nil)
		})

	sc.Step(`^the expression "([^"]*)" is evaluated (\d+) times with "([^"]*)" bound to (.+)$`,
		func(ctx context.Context, src string, n int, varName, raw string) error {
			val, err := parseJSONValue(strings.TrimSpace(raw))
			if err != nil {
				return err
			}
			world(ctx).Expr.bindings[varName] = val
			return exprEval(ctx, src, n, "", nil)
		})

	sc.Step(`^the expression "([^"]*)" is evaluated again with "([^"]*)" bound to (.+)$`,
		func(ctx context.Context, src, varName, raw string) error {
			val, err := parseJSONValue(strings.TrimSpace(raw))
			if err != nil {
				return err
			}
			world(ctx).Expr.bindings[varName] = val
			return exprEvalAgain(ctx, src)
		})

	sc.Step(`^"([^"]*)" is bound to (.+), "([^"]*)" to (.+) and "([^"]*)" to (.+)$`,
		func(ctx context.Context, n1, v1, n2, v2, n3, v3 string) error {
			e := world(ctx).Expr
			for _, pair := range []struct {
				name, raw string
			}{{n1, v1}, {n2, v2}, {n3, v3}} {
				val, err := parseJSONValue(strings.TrimSpace(pair.raw))
				if err != nil {
					return err
				}
				e.bindings[pair.name] = val
			}
			return nil
		})

	sc.Step(`^evaluation fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		err := world(ctx).Expr.lastErr
		if err == nil {
			return fmt.Errorf("evaluation succeeded, want %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s: %w", got, code, err)
		}
		return nil
	})

	sc.Step(`^the error names the expression and the cost limit (\d+)$`, func(ctx context.Context, limit int) error {
		e := world(ctx).Expr
		return exprMsgContainsErr(e.lastErr, e.lastExpr, strconv.Itoa(limit), "cost limit")
	})

	sc.Step(`^the error names the expression and the cause "([^"]*)"$`, func(ctx context.Context, cause string) error {
		e := world(ctx).Expr
		return exprMsgContainsErr(e.lastErr, e.lastExpr, cause)
	})

	sc.Step(`^the error names the variable "([^"]*)"$`, func(ctx context.Context, name string) error {
		err := world(ctx).Expr.lastErr
		if err == nil {
			return fmt.Errorf("no error, want variable %q", name)
		}
		msg := errMsg(err)
		if !strings.Contains(msg, name) {
			return fmt.Errorf("message %q missing variable %q", msg, name)
		}
		return nil
	})

	sc.Step(`^the result is ((?:[\[{"<]|true|false|null|-?[0-9]).*)$`, func(ctx context.Context, raw string) error {
		want, err := parseJSONValue(strings.TrimSpace(raw))
		if err != nil {
			return err
		}
		return jsonValuesEqual(world(ctx).Expr.result, want)
	})

	sc.Step(`^both results are (\d+)$`, func(ctx context.Context, n int) error {
		e := world(ctx).Expr
		if e.lastErr != nil {
			return fmt.Errorf("evaluation failed: %w", e.lastErr)
		}
		if e.evalTimes < 2 {
			return fmt.Errorf("expected 2 evaluations, got %d", e.evalTimes)
		}
		for i, r := range e.evalResults[:] {
			if r == nil {
				return fmt.Errorf("missing eval result %d", i)
			}
			f, ok := asNumber(r)
			if !ok || int(f) != n {
				return fmt.Errorf("result %d got %v, want %d", i, r, n)
			}
		}
		return nil
	})

	sc.Step(`^every result is (.+)$`, func(ctx context.Context, raw string) error {
		e := world(ctx).Expr
		if e.lastErr != nil {
			return fmt.Errorf("evaluation failed: %w", e.lastErr)
		}
		if e.expectedEvalCount == 0 {
			return fmt.Errorf("no evaluations recorded")
		}
		if len(e.results) != e.expectedEvalCount {
			return fmt.Errorf("got %d results, want %d", len(e.results), e.expectedEvalCount)
		}
		want, err := parseJSONValue(strings.TrimSpace(raw))
		if err != nil {
			return err
		}
		for i, r := range e.results {
			if err := jsonValuesEqual(r, want); err != nil {
				return fmt.Errorf("result %d: %w", i, err)
			}
		}
		return nil
	})

	// Mappings
	sc.Step(`^the mapping "(.+?)"(?: with variable "([^"]*)")?$`, func(ctx context.Context, src, varName string) error {
		var vars []string
		if varName != "" {
			vars = []string{varName}
		}
		return exprMappingDeclare(ctx, src, vars, nil)
	})

	sc.Step(`^the mapping "(.+)" with a step limit of (\d+)$`, func(ctx context.Context, src string, limit uint64) error {
		return exprMappingDeclare(ctx, src, nil, &limit)
	})

	sc.Step(`^the mapping "(.+)" with an output limit of (\d+) bytes$`, exprMappingDeclareOutput)

	sc.Step(`^the mapping "(.+)" is compiled$`, func(ctx context.Context, src string) error {
		e := world(ctx).Expr
		_, err := expr.CompileMapping(src, nil)
		e.mapCompile = err
		if err != nil {
			e.lastErr = err
		}
		return nil
	})

	sc.Step(`^the mapping is evaluated on (.+)$`, func(ctx context.Context, rest string) error {
		rest = strings.TrimSpace(rest)
		var raw, vname, vraw string
		if i := strings.Index(rest, ` with "`); i >= 0 {
			suffix := rest[i+len(` with "`):]
			j := strings.Index(suffix, `" bound to `)
			if j >= 0 {
				raw = strings.TrimSpace(rest[:i])
				vname = suffix[:j]
				vraw = strings.TrimSpace(suffix[j+len(`" bound to `):])
			}
		}
		if raw == "" {
			raw = rest
		}
		in, err := parseJSONValue(raw)
		if err != nil {
			return err
		}
		if vname != "" {
			vval, err := parseJSONValue(vraw)
			if err != nil {
				return err
			}
			world(ctx).Expr.bindings[vname] = vval
		}
		return exprMappingEval(ctx, in, 1)
	})

	sc.Step(`^the mapping is evaluated (\d+) times on (.+)$`, func(ctx context.Context, n int, raw string) error {
		in, err := parseJSONValue(strings.TrimSpace(raw))
		if err != nil {
			return err
		}
		return exprMappingEval(ctx, in, n)
	})

	sc.Step(`^the error states that the mapping produced (.+)$`, func(ctx context.Context, phrase string) error {
		msg := strings.ToLower(errMsg(world(ctx).Expr.lastErr))
		switch strings.TrimSpace(phrase) {
		case "no value":
			if !strings.Contains(msg, "no value") {
				return fmt.Errorf("message %q does not say no value", msg)
			}
		case "more than one value":
			if !strings.Contains(msg, "more than one value") {
				return fmt.Errorf("message %q does not say more than one value", msg)
			}
		default:
			return fmt.Errorf("unknown count phrase %q", phrase)
		}
		return nil
	})

	sc.Step(`^the error names "([^"]*)"$`, func(ctx context.Context, name string) error {
		return exprMsgContains(ctx, name)
	})

	sc.Step(`^the error names "([^"]*)" and says the name is reserved$`, func(ctx context.Context, name string) error {
		return exprMsgContains(ctx, name, "reserved")
	})

	sc.Step(`^the error names the reserved variable "([^"]*)"$`, func(ctx context.Context, name string) error {
		return exprMsgContains(ctx, name, "reserved")
	})

	sc.Step(`^the error names the step limit (\d+)$`, func(ctx context.Context, limit int) error {
		return exprMsgContains(ctx, strconv.Itoa(limit))
	})

	sc.Step(`^the error names the output limit (\d+)$`, func(ctx context.Context, limit int) error {
		return exprMsgContains(ctx, "output limit", strconv.Itoa(limit))
	})

	sc.Step(`^the result is written compactly in (\d+) bytes$`, func(ctx context.Context, length int) error {
		e := world(ctx).Expr
		if e.lastErr != nil {
			return fmt.Errorf("evaluation failed: %w", e.lastErr)
		}
		b, err := json.Marshal(e.result)
		if err != nil {
			return err
		}
		if len(b) != length {
			return fmt.Errorf("result is %d bytes, want %d: %s", len(b), length, b)
		}
		return nil
	})

	sc.Step(`^the error names the mapping and the position of the error$`, func(ctx context.Context) error {
		e := world(ctx).Expr
		if e.mapCompile == nil {
			return fmt.Errorf("no mapping compile error")
		}
		msg := errMsg(e.mapCompile)
		if !strings.Contains(msg, "mapping") {
			return fmt.Errorf("message does not name mapping: %s", msg)
		}
		if !strings.Contains(msg, "offset") && !strings.Contains(msg, "position") {
			return fmt.Errorf("message does not name position: %s", msg)
		}
		return nil
	})

}
