package authz

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/expr"
)

const defaultConditionCacheSize = 256

var conditionResultSchema = json.RawMessage(`{"type":"boolean"}`)

var conditionEnvSchema = []expr.Var{
	{
		Name: "principal",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string"},
    "scopes": {"type": "array", "items": {"type": "string"}}
  }
}`),
	},
	{
		Name: "resource",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "project": {"type": "string"},
    "kind": {"type": "string"},
    "id": {"type": "string"},
    "attrs": {"type": "object", "additionalProperties": true}
  }
}`),
	},
}

type condEval int

const (
	condNone condEval = iota
	condTrue
	condFalse
	condError
	condCost
)

type conditionCompiler struct {
	env       *expr.Env
	costLimit uint64
}

func newConditionCompiler(costLimit uint64) (*conditionCompiler, error) {
	env, err := expr.NewEnv(conditionEnvSchema, expr.WithCostLimit(costLimit))
	if err != nil {
		return nil, err
	}
	return &conditionCompiler{env: env, costLimit: costLimit}, nil
}

func (c *conditionCompiler) compileNew(text string) (*expr.Program, error) {
	p, err := c.env.Compile(text, conditionResultSchema)
	if err != nil {
		return nil, errInvalidCondition()
	}
	return p, nil
}

// isCostErr reports a cost-limit breach from expr evaluation. expr currently
// signals this only through a suffix on the error string (see expr/program.go
// mapEvalError); a typed error would be preferable (ticket pending).
func (c *conditionCompiler) isCostErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.HasSuffix(err.Error(), fmt.Sprintf("cost limit %d exceeded", c.costLimit))
}

type conditionCache struct {
	mu    sync.Mutex
	max   int
	order []string
	m     map[string]*expr.Program
	err   map[string]error
}

func newConditionCache(max int) *conditionCache {
	return &conditionCache{
		max: max,
		m:   make(map[string]*expr.Program),
		err: make(map[string]error),
	}
}

func (c *conditionCache) get(text string, compile func(string) (*expr.Program, error)) (*expr.Program, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.m[text]; ok {
		c.touch(text)
		return p, nil
	}
	if e, ok := c.err[text]; ok {
		c.touch(text)
		return nil, e
	}
	p, err := compile(text)
	if err != nil {
		if len(c.order) >= c.max {
			c.evictOldest()
		}
		c.err[text] = err
		c.order = append(c.order, text)
		return nil, err
	}
	if len(c.order) >= c.max {
		c.evictOldest()
	}
	c.m[text] = p
	c.order = append(c.order, text)
	return p, nil
}

func (c *conditionCache) touch(key string) {
	for i, k := range c.order {
		if k == key {
			copy(c.order[i:], c.order[i+1:])
			c.order[len(c.order)-1] = key
			return
		}
	}
}

func (c *conditionCache) evictOldest() {
	if len(c.order) == 0 {
		return
	}
	old := c.order[0]
	c.order = c.order[1:]
	delete(c.m, old)
	delete(c.err, old)
}

func (c *conditionCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.order)
}

func evalCondition(p *expr.Program, comp *conditionCompiler, principal authn.Principal, resource Resource) condEval {
	vars := map[string]any{
		"principal": map[string]any{
			"id":     principal.ID,
			"scopes": scopesToAny(principal.Scopes),
		},
		"resource": map[string]any{
			"project": resource.Project,
			"kind":    resource.Kind,
			"id":      resource.ID,
			"attrs":   resource.Attrs,
		},
	}
	if p == nil {
		return condError
	}
	out, err := p.Eval(vars)
	if err != nil {
		if comp.isCostErr(err) {
			return condCost
		}
		return condError
	}
	b, ok := out.(bool)
	if !ok {
		return condError
	}
	if b {
		return condTrue
	}
	return condFalse
}

func scopesToAny(scopes []string) []any {
	if len(scopes) == 0 {
		return []any{}
	}
	out := make([]any, len(scopes))
	for i, s := range scopes {
		out[i] = s
	}
	return out
}

func worstConditionReason(a, b string) string {
	ra, rb := conditionReasonRank(a), conditionReasonRank(b)
	if ra >= rb {
		return a
	}
	return b
}

func conditionReasonRank(r string) int {
	switch r {
	case ReasonConditionCostExceed:
		return 3
	case ReasonConditionError:
		return 2
	case ReasonNoBinding:
		return 1
	default:
		return 0
	}
}
