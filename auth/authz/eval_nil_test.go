package authz

import (
	"database/sql"
	"testing"

	"github.com/epzero/basamento/auth/authn"
)

func TestEvalConditionNilProgram(t *testing.T) {
	comp, err := newConditionCompiler(defaultCostLimit)
	if err != nil {
		t.Fatal(err)
	}
	ev := evalCondition(nil, comp, authn.Principal{ID: "b"}, Resource{Kind: "run", ID: "r1"})
	if ev != condError {
		t.Fatalf("got %v", ev)
	}
}

func TestEvalBindingNilFromCache(t *testing.T) {
	comp, err := newConditionCompiler(defaultCostLimit)
	if err != nil {
		t.Fatal(err)
	}
	a := &Authorizer{
		compile: comp,
		cache:   newConditionCache(4),
		roles:   testRoles(),
	}
	a.cache.m["x"] = nil
	a.cache.order = []string{"x"}
	var cond sql.NullString
	cond.String = "x"
	cond.Valid = true
	hold, reason := a.evalBinding(authn.Principal{ID: "b"}, Resource{Kind: "run", ID: "r1"}, BindingRow{Condition: cond})
	if hold || reason != ReasonConditionError {
		t.Fatalf("hold=%v reason=%q", hold, reason)
	}
}
