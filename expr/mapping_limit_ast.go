package expr

import (
	"fmt"
	"strings"

	"github.com/itchyny/gojq"
)

// Names of the bounded functions the output-limit rewrite calls. They start
// with `_` so that no user function shares their shape; a definition,
// parameter or call of one is rejected like a banned builtin.
const (
	boundedAdd     = "_outadd"
	boundedSub     = "_outsub"
	boundedMul     = "_outmul"
	boundedArray   = "_outarray"
	boundedObject  = "_outobject"
	boundedJoin    = "_outjoin"
	boundedImplode = "_outimplode"
	boundedBase64  = "_outbase64"
	boundedBase64d = "_outbase64d"
)

// outputLimitReserved names everything a mapping may not call, define or bind
// (REQ-EXPR-020): the gojq operators the rewrite replaces, and the bounded
// functions themselves.
var outputLimitReserved = map[string]bool{
	"_add":         true,
	"_subtract":    true,
	"_multiply":    true,
	boundedAdd:     true,
	boundedSub:     true,
	boundedMul:     true,
	boundedArray:   true,
	boundedObject:  true,
	boundedJoin:    true,
	boundedImplode: true,
	boundedBase64:  true,
	boundedBase64d: true,
}

func outputLimitReservedName(name string) bool {
	return outputLimitReserved[name]
}

// mappingRewriter rewrites a parsed mapping before compilation so that the
// operations which can build a value larger than the output limit run through
// bounded functions (REQ-EXPR-020, design.md §5).
type mappingRewriter struct {
	src      string
	tempVar  int
	usedVars map[string]bool
	// shadowed counts user definitions and parameters in scope, so a local
	// `join` or `implode` is not replaced by a bounded builtin.
	shadowed map[string]int
}

func newMappingRewriter(src string, varNames []string) *mappingRewriter {
	used := make(map[string]bool, len(varNames))
	for _, name := range varNames {
		used[name] = true
	}
	return &mappingRewriter{src: src, usedVars: used, shadowed: map[string]int{}}
}

// rewriteOutputLimit rewrites root in place.
func (r *mappingRewriter) rewriteOutputLimit(root *gojq.Query) error {
	if root == nil {
		return nil
	}
	return r.query(root)
}

// freshVariable returns a variable name the mapping's source does not mention.
func (r *mappingRewriter) freshVariable() string {
	for {
		name := fmt.Sprintf("$__outlimit%d", r.tempVar)
		r.tempVar++
		if r.usedVars[name] || strings.Contains(r.src, name) {
			continue
		}
		r.usedVars[name] = true
		return name
	}
}

func (r *mappingRewriter) query(q *gojq.Query) error {
	if q == nil {
		return nil
	}
	pop := r.pushDefinitions(q)
	defer pop()
	for _, fd := range q.FuncDefs {
		if err := r.query(fd.Body); err != nil {
			return err
		}
	}
	if q.Term != nil {
		return r.term(q.Term)
	}
	switch q.Op {
	case gojq.OpUpdateAdd, gojq.OpUpdateSub, gojq.OpUpdateMul:
		// l op= r becomes (r) as $__value | (l |= . op $__value), so the
		// operator inside the update is rewritten by the operator rule below.
		name := r.freshVariable()
		op := gojq.OpAdd
		switch q.Op {
		case gojq.OpUpdateSub:
			op = gojq.OpSub
		case gojq.OpUpdateMul:
			op = gojq.OpMul
		}
		replacement := &gojq.Query{
			Meta:     q.Meta,
			Imports:  q.Imports,
			FuncDefs: q.FuncDefs,
			Left:     &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeQuery, Query: q.Right}},
			Op:       gojq.OpPipe,
			Patterns: []*gojq.Pattern{{Name: name}},
			Right: &gojq.Query{
				Left: q.Left,
				Op:   gojq.OpModify,
				Right: &gojq.Query{
					Left:  identityQuery(),
					Op:    op,
					Right: variableQuery(name),
				},
			},
		}
		*q = *replacement
		return r.query(q)
	case gojq.OpAdd, gojq.OpSub, gojq.OpMul:
		op, operands := flattenArithmetic(q)
		for _, operand := range operands {
			if err := r.query(operand); err != nil {
				return err
			}
		}
		name := boundedAdd
		switch op {
		case gojq.OpSub:
			name = boundedSub
		case gojq.OpMul:
			name = boundedMul
		}
		call := boundedChain(name, operands)
		call.Meta, call.Imports, call.FuncDefs = q.Meta, q.Imports, q.FuncDefs
		*q = *call
		return nil
	default:
		if err := r.query(q.Left); err != nil {
			return err
		}
		return r.query(q.Right)
	}
}

// pushDefinitions makes a query's function definitions and their parameters
// visible while its operands are rewritten, and returns the matching pop.
func (r *mappingRewriter) pushDefinitions(q *gojq.Query) func() {
	pushed := make([]string, 0, len(q.FuncDefs))
	for _, fd := range q.FuncDefs {
		r.shadowed[fd.Name]++
		pushed = append(pushed, fd.Name)
	}
	for _, fd := range q.FuncDefs {
		for _, arg := range fd.Args {
			r.shadowed[arg]++
			pushed = append(pushed, arg)
		}
	}
	return func() {
		for _, name := range pushed {
			r.shadowed[name]--
			if r.shadowed[name] == 0 {
				delete(r.shadowed, name)
			}
		}
	}
}

func (r *mappingRewriter) isShadowed(name string) bool {
	return r.shadowed[name] > 0
}

// flattenArithmetic collects the operands of a left-associative chain of one
// operator. A parenthesized or otherwise mixed right operand stays one operand
// and is rewritten on its own.
func flattenArithmetic(q *gojq.Query) (gojq.Operator, []*gojq.Query) {
	op := q.Op
	operands := make([]*gojq.Query, 0, 2)
	var walk func(*gojq.Query)
	walk = func(n *gojq.Query) {
		if n.Term == nil && n.Op == op && len(n.FuncDefs) == 0 && len(n.Patterns) == 0 {
			walk(n.Left)
			operands = append(operands, n.Right)
			return
		}
		operands = append(operands, n)
	}
	walk(q)
	return op, operands
}

// boundedChain folds operands into calls of at most thirty arguments, gojq's
// limit for a custom function.
func boundedChain(name string, operands []*gojq.Query) *gojq.Query {
	call := operands[0]
	rest := operands[1:]
	for len(rest) > 0 {
		n := len(rest)
		if n > 29 {
			n = 29
		}
		args := make([]*gojq.Query, 0, n+1)
		args = append(args, call)
		args = append(args, rest[:n]...)
		call = funcQuery(name, args...)
		rest = rest[n:]
	}
	return call
}

func (r *mappingRewriter) term(t *gojq.Term) error {
	if t == nil {
		return nil
	}
	switch t.Type {
	case gojq.TermTypeIndex:
		if err := r.query(t.Index.Start); err != nil {
			return err
		}
		if err := r.query(t.Index.End); err != nil {
			return err
		}
		if err := r.stringQueries(t.Index.Str); err != nil {
			return err
		}
	case gojq.TermTypeFunc:
		for _, arg := range t.Func.Args {
			if err := r.query(arg); err != nil {
				return err
			}
		}
		switch {
		case t.Func.Name == "join" && len(t.Func.Args) == 1 && !r.isShadowed("join"):
			t.Func.Name = boundedJoin
			t.Func.Args = append([]*gojq.Query{identityQuery()}, t.Func.Args...)
		case t.Func.Name == "implode" && len(t.Func.Args) == 0 && !r.isShadowed("implode"):
			t.Func = &gojq.Func{Name: boundedImplode, Args: []*gojq.Query{identityQuery()}}
		}
	case gojq.TermTypeObject:
		for _, kv := range t.Object.KeyVals {
			if err := r.query(kv.KeyQuery); err != nil {
				return err
			}
			if err := r.query(kv.Val); err != nil {
				return err
			}
			if err := r.stringQueries(kv.KeyString); err != nil {
				return err
			}
		}
		obj := &gojq.Term{Type: gojq.TermTypeObject, Object: t.Object}
		suffix := t.SuffixList
		*t = gojq.Term{
			Type:       gojq.TermTypeFunc,
			Func:       &gojq.Func{Name: boundedObject, Args: []*gojq.Query{{Term: obj}}},
			SuffixList: suffix,
		}
		return nil
	case gojq.TermTypeArray:
		if err := r.query(t.Array.Query); err != nil {
			return err
		}
		arr := &gojq.Term{Type: gojq.TermTypeArray, Array: t.Array}
		suffix := t.SuffixList
		*t = gojq.Term{
			Type:       gojq.TermTypeFunc,
			Func:       &gojq.Func{Name: boundedArray, Args: []*gojq.Query{{Term: arr}}},
			SuffixList: suffix,
		}
		return nil
	case gojq.TermTypeFormat:
		if t.Format == "@base64" || t.Format == "@base64d" {
			name := boundedBase64
			if t.Format == "@base64d" {
				name = boundedBase64d
			}
			return r.rewriteFormat(t, name)
		}
		if err := r.stringQueries(t.Str); err != nil {
			return err
		}
	case gojq.TermTypeString:
		if err := r.stringQueries(t.Str); err != nil {
			return err
		}
	case gojq.TermTypeUnary:
		if err := r.term(t.Unary.Term); err != nil {
			return err
		}
	case gojq.TermTypeIf:
		if err := r.ifNode(t.If); err != nil {
			return err
		}
	case gojq.TermTypeTry:
		if err := r.query(t.Try.Body); err != nil {
			return err
		}
		if err := r.query(t.Try.Catch); err != nil {
			return err
		}
	case gojq.TermTypeReduce:
		if err := r.query(t.Reduce.Query); err != nil {
			return err
		}
		if err := r.query(t.Reduce.Start); err != nil {
			return err
		}
		if err := r.query(t.Reduce.Update); err != nil {
			return err
		}
	case gojq.TermTypeForeach:
		if err := r.query(t.Foreach.Query); err != nil {
			return err
		}
		if err := r.query(t.Foreach.Start); err != nil {
			return err
		}
		if err := r.query(t.Foreach.Update); err != nil {
			return err
		}
		if err := r.query(t.Foreach.Extract); err != nil {
			return err
		}
	case gojq.TermTypeLabel:
		if err := r.query(t.Label.Body); err != nil {
			return err
		}
	case gojq.TermTypeQuery:
		if err := r.query(t.Query); err != nil {
			return err
		}
	}
	for _, s := range t.SuffixList {
		if s.Index == nil {
			continue
		}
		if err := r.query(s.Index.Start); err != nil {
			return err
		}
		if err := r.query(s.Index.End); err != nil {
			return err
		}
		if err := r.stringQueries(s.Index.Str); err != nil {
			return err
		}
	}
	return nil
}

// rewriteFormat turns @base64 and @base64d into their bounded functions. With
// an interpolated string jq formats each expression, not the whole string, so
// each non-literal part is wrapped and the term becomes a string again.
func (r *mappingRewriter) rewriteFormat(t *gojq.Term, name string) error {
	if t.Str == nil {
		suffix := t.SuffixList
		*t = gojq.Term{
			Type:       gojq.TermTypeFunc,
			Func:       &gojq.Func{Name: name, Args: []*gojq.Query{identityQuery()}},
			SuffixList: suffix,
		}
		return nil
	}
	for i, q := range t.Str.Queries {
		if err := r.query(q); err != nil {
			return err
		}
		if q.Term != nil && q.Term.Type == gojq.TermTypeString && q.Term.Str != nil && q.Term.Str.Queries == nil {
			continue
		}
		t.Str.Queries[i] = funcQuery(name, q)
	}
	t.Type = gojq.TermTypeString
	t.Format = ""
	return nil
}

func (r *mappingRewriter) ifNode(n *gojq.If) error {
	if n == nil {
		return nil
	}
	if err := r.query(n.Cond); err != nil {
		return err
	}
	if err := r.query(n.Then); err != nil {
		return err
	}
	for _, elif := range n.Elif {
		if err := r.query(elif.Cond); err != nil {
			return err
		}
		if err := r.query(elif.Then); err != nil {
			return err
		}
	}
	return r.query(n.Else)
}

func (r *mappingRewriter) stringQueries(s *gojq.String) error {
	if s == nil {
		return nil
	}
	for _, q := range s.Queries {
		if err := r.query(q); err != nil {
			return err
		}
	}
	return nil
}

func identityQuery() *gojq.Query {
	return &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeIdentity}}
}

func variableQuery(name string) *gojq.Query {
	return &gojq.Query{Term: &gojq.Term{
		Type: gojq.TermTypeFunc,
		Func: &gojq.Func{Name: name},
	}}
}

func funcQuery(name string, args ...*gojq.Query) *gojq.Query {
	return &gojq.Query{Term: &gojq.Term{
		Type: gojq.TermTypeFunc,
		Func: &gojq.Func{Name: name, Args: args},
	}}
}
