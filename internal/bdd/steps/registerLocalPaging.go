package steps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/runtime"
)

// localPagingRecorder holds list paging outcomes for RT-051…053 scenarios.
type localPagingRecorder struct {
	pages      [][]runtime.RunStatus
	cursors    []string
	heldCursor string
	listFlow   string
	listLimit  int
}

func (w *localWorld) pagingState() *localPagingRecorder {
	if w.paging == nil {
		w.paging = &localPagingRecorder{}
	}
	return w.paging
}

func registerLocalPaging(sc *godog.ScenarioContext) {
	sc.Step(`^tenant "([^"]*)" has runs (.+) of flow "([^"]*)" waiting at gate "([^"]*)"$`,
		startRunsOfFlowWaitingAtGate)
	sc.Step(`^tenant "([^"]*)" lists runs of flow "([^"]*)" with limit (\d+) and no paging cursor$`,
		listRunsWithLimitNoCursor)
	sc.Step(`^tenant "([^"]*)" lists the next page with the paging cursor from that listing$`,
		listNextPageFromCursor)
	sc.Step(`^the first page lists run ids "([^"]*)" then "([^"]*)"$`, assertFirstPageRunIDs)
	sc.Step(`^the second page lists run id "([^"]*)"$`, assertSecondPageRunID)
	sc.Step(`^the two pages share no run id$`, assertTwoPagesDisjoint)
	sc.Step(`^tenant "([^"]*)" lists runs of flow "([^"]*)" with the paging cursor "([^"]*)"$`,
		listRunsWithDescribedCursor)
	sc.Step(`^tenant "([^"]*)" has a paging cursor for flow "([^"]*)" limited to (\d+) run$`,
		capturePagingCursor)
	sc.Step(`^tenant "([^"]*)" lists runs of flow "([^"]*)" with that paging cursor$`,
		listRunsWithHeldCursor)
}

func parseLocalRunIDList(phrase string) []string {
	phrase = strings.TrimSpace(phrase)
	var ids []string
	for {
		i := strings.Index(phrase, `"`)
		if i < 0 {
			break
		}
		phrase = phrase[i+1:]
		j := strings.Index(phrase, `"`)
		if j < 0 {
			break
		}
		ids = append(ids, phrase[:j])
		phrase = phrase[j+1:]
		phrase = strings.TrimSpace(phrase)
		phrase = strings.TrimPrefix(phrase, ",")
		phrase = strings.TrimPrefix(phrase, "and")
		phrase = strings.TrimSpace(phrase)
	}
	return ids
}

func startRunsOfFlowWaitingAtGate(ctx context.Context, tenant, runIDs, flow, gate string) error {
	w := localStepWorld(ctx)
	w.setFlow(flow, w.gateFlow(gate))
	for _, runID := range parseLocalRunIDList(runIDs) {
		_, exec, err := w.start(ctx, tenant, runID, flow)
		if err != nil {
			return err
		}
		if err := exec.wait("the run to await its gate", func() bool { return exec.atGate != "" }); err != nil {
			return err
		}
	}
	return nil
}

func listRunsWithLimitNoCursor(ctx context.Context, tenant, flow string, limit int) error {
	w := localStepWorld(ctx)
	ps := w.pagingState()
	ps.listFlow = flow
	ps.listLimit = limit
	runs, cursor, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow, Limit: limit})
	ps.pages = append(ps.pages, runs)
	ps.cursors = append(ps.cursors, cursor)
	w.lastList, w.lastErr = runs, err
	return nil
}

func listNextPageFromCursor(ctx context.Context, tenant string) error {
	w := localStepWorld(ctx)
	ps := w.pagingState()
	if len(ps.cursors) == 0 || ps.cursors[len(ps.cursors)-1] == "" {
		return fmt.Errorf("the previous listing returned no paging cursor")
	}
	cursor := ps.cursors[len(ps.cursors)-1]
	runs, next, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{
		Flow:   ps.listFlow,
		Limit:  ps.listLimit,
		Cursor: cursor,
	})
	ps.pages = append(ps.pages, runs)
	ps.cursors = append(ps.cursors, next)
	w.lastList, w.lastErr = runs, err
	return nil
}

func assertFirstPageRunIDs(ctx context.Context, id1, id2 string) error {
	ps := localStepWorld(ctx).pagingState()
	if len(ps.pages) < 1 {
		return fmt.Errorf("no list page recorded")
	}
	want := []string{id1, id2}
	got := runIDsOf(ps.pages[0])
	if len(got) != len(want) {
		return fmt.Errorf("first page lists %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("first page lists %v, want %v", got, want)
		}
	}
	return nil
}

func assertSecondPageRunID(ctx context.Context, id string) error {
	ps := localStepWorld(ctx).pagingState()
	if len(ps.pages) < 2 {
		return fmt.Errorf("no second list page recorded")
	}
	got := runIDsOf(ps.pages[1])
	if len(got) != 1 || got[0] != id {
		return fmt.Errorf("second page lists %v, want [%q]", got, id)
	}
	return nil
}

func assertTwoPagesDisjoint(ctx context.Context) error {
	ps := localStepWorld(ctx).pagingState()
	if len(ps.pages) < 2 {
		return fmt.Errorf("need two list pages")
	}
	a, b := runIDsOf(ps.pages[0]), runIDsOf(ps.pages[1])
	set := map[string]struct{}{}
	for _, id := range a {
		set[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := set[id]; ok {
			return fmt.Errorf("pages share run id %q", id)
		}
	}
	return nil
}

func listRunsWithDescribedCursor(ctx context.Context, tenant, flow, description string) error {
	w := localStepWorld(ctx)
	switch description {
	case "a paging cursor the runtime never issued":
		runs, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{
			Flow:   flow,
			Cursor: description,
		})
		w.lastList, w.lastErr = runs, err
		return nil
	case "a paging cursor past the last run":
		valid := craftListCursorToken(tenant, 0)
		control, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow, Cursor: valid})
		if err != nil {
			return fmt.Errorf("control list at valid cursor: %w", err)
		}
		if len(control) != 1 || control[0].Ref.RunID != "r1" {
			return fmt.Errorf("control list = %#v, want [r1]", control)
		}
		beyond := craftListCursorToken(tenant, 99)
		runs, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow, Cursor: beyond})
		w.lastList, w.lastErr = runs, err
		return nil
	default:
		return fmt.Errorf("unknown paging cursor description %q", description)
	}
}

func capturePagingCursor(ctx context.Context, tenant, flow string, limit int) error {
	w := localStepWorld(ctx)
	ps := w.pagingState()
	ps.listFlow = flow
	ps.listLimit = limit
	_, cursor, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow, Limit: limit})
	if err != nil {
		return err
	}
	if cursor == "" {
		return fmt.Errorf("listing returned no paging cursor")
	}
	ps.heldCursor = cursor
	return nil
}

func listRunsWithHeldCursor(ctx context.Context, tenant, flow string) error {
	w := localStepWorld(ctx)
	ps := w.pagingState()
	if ps.heldCursor == "" {
		return fmt.Errorf("no paging cursor held")
	}
	runs, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow, Cursor: ps.heldCursor})
	w.lastList, w.lastErr = runs, err
	return nil
}

func runIDsOf(page []runtime.RunStatus) []string {
	ids := make([]string, len(page))
	for i, st := range page {
		ids[i] = st.Ref.RunID
	}
	return ids
}

type listCursorWire struct {
	V      int    `json:"v"`
	Tenant string `json:"tenant"`
	Pos    int    `json:"pos"`
}

// craftListCursorToken builds the documented list cursor form for BDD preconditions.
func craftListCursorToken(tenant string, pos int) string {
	raw, err := json.Marshal(listCursorWire{V: 1, Tenant: tenant, Pos: pos})
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}
