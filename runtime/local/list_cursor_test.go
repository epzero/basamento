package local

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestListCursorWrongTenantInRangeTwoEqualTenants(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "a1", "noop", `{}`)
	f.waitState(t, "acme", "a1", stateCompleted)
	f.start(t, "globex", "g1", "noop", `{}`)
	f.waitState(t, "globex", "g1", stateCompleted)

	tok := listCursorPositionForTests("acme", 0)
	page, _, err := f.rt.List(tenantCtx("globex"), runtime.RunQuery{Cursor: tok})
	if codeOf(err) != "invalid_argument" {
		t.Fatalf("cross-tenant in-range = %v, want invalid_argument", err)
	}
	if len(page) != 0 {
		t.Fatalf("listed %v, want none", page)
	}
}
