package local

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestListPaging(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	for i := 1; i <= 5; i++ {
		f.start(t, "acme", fmt.Sprintf("r%d", i), "noop", `{}`)
		f.waitState(t, "acme", fmt.Sprintf("r%d", i), stateCompleted)
	}
	ctx := tenantCtx("acme")
	var seen []string
	cursor := ""
	for page := 0; page < 3; page++ {
		pageRuns, next, err := f.rt.List(ctx, runtime.RunQuery{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: List: %v", page, err)
		}
		for _, st := range pageRuns {
			seen = append(seen, st.Ref.RunID)
		}
		cursor = next
		if page < 2 && cursor == "" {
			t.Fatalf("page %d: expected a next cursor", page)
		}
		if page == 2 && cursor != "" {
			t.Fatalf("final page cursor %q, want empty", cursor)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("listed %d runs, want 5", len(seen))
	}
	if len(dedup(seen)) != 5 {
		t.Fatalf("duplicate ids in %v", seen)
	}
}

func dedup(ids []string) []string {
	set := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := set[id]; ok {
			continue
		}
		set[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func TestListPagingRespectsFilters(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("a", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.flow("b", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "a", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	f.start(t, "acme", "r2", "b", `{}`)
	f.waitState(t, "acme", "r2", stateCompleted)
	f.start(t, "acme", "r3", "a", `{}`)
	f.waitState(t, "acme", "r3", stateCompleted)

	ctx := tenantCtx("acme")
	page, next, err := f.rt.List(ctx, runtime.RunQuery{Flow: "a", Limit: 1})
	if err != nil || len(page) != 1 || page[0].Ref.RunID != "r1" || next == "" {
		t.Fatalf("first page = %#v, %q, %v", page, next, err)
	}
	page, next, err = f.rt.List(ctx, runtime.RunQuery{Flow: "a", Limit: 1, Cursor: next})
	if err != nil || len(page) != 1 || page[0].Ref.RunID != "r3" || next != "" {
		t.Fatalf("second page = %#v, %q, %v", page, next, err)
	}
}

func TestListInvalidCursor(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "noop", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	ctx := tenantCtx("acme")
	if _, _, err := f.rt.List(ctx, runtime.RunQuery{Cursor: "not-a-token"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("bad cursor = %v, want invalid_argument", err)
	}
	pastEnd := listCursorPositionForTests("acme", 1)
	if _, _, err := f.rt.List(ctx, runtime.RunQuery{Cursor: pastEnd}); codeOf(err) != "invalid_argument" {
		t.Fatalf("out of range cursor = %v, want invalid_argument", err)
	}
}

func TestListLimitLargerThanList(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "noop", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	page, next, err := f.rt.List(tenantCtx("acme"), runtime.RunQuery{Limit: 100})
	if err != nil || len(page) != 1 || next != "" {
		t.Fatalf("List = %#v, %q, %v", page, next, err)
	}
}

func TestListCursorDoesNotWidenTenantVisibility(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "a1", "noop", `{}`)
	f.waitState(t, "acme", "a1", stateCompleted)
	f.start(t, "globex", "g1", "noop", `{}`)
	f.waitState(t, "globex", "g1", stateCompleted)
	f.start(t, "acme", "a2", "noop", `{}`)
	f.waitState(t, "acme", "a2", stateCompleted)

	page, next, err := f.rt.List(tenantCtx("acme"), runtime.RunQuery{Limit: 1})
	if err != nil || len(page) != 1 || page[0].Ref.RunID != "a1" {
		t.Fatalf("first acme page = %#v, %v", page, err)
	}
	page, next, err = f.rt.List(tenantCtx("acme"), runtime.RunQuery{Limit: 1, Cursor: next})
	if err != nil || len(page) != 1 || page[0].Ref.RunID != "a2" || next != "" {
		t.Fatalf("second acme page = %#v, %q, %v", page, next, err)
	}
}
