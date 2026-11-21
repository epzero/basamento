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
	pages        [][]runtime.RunStatus
	cursors      []string
	heldCursor   string
	listFlow     string
	listLimit    int
	seededRunIDs []string
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

func parseStrictRunIDList(phrase string) ([]string, error) {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		return nil, fmt.Errorf("run id list is empty")
	}
	var ids []string
	rest := phrase
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, `"`) {
			return nil, fmt.Errorf("run id list %q: expected a quoted id, got %q", phrase, rest)
		}
		rest = rest[1:]
		j := strings.Index(rest, `"`)
		if j < 0 {
			return nil, fmt.Errorf("run id list %q: unclosed quote", phrase)
		}
		ids = append(ids, rest[:j])
		rest = strings.TrimSpace(rest[j+1:])
		if rest == "" {
			break
		}
		switch {
		case strings.HasPrefix(rest, "and "):
			rest = strings.TrimPrefix(rest, "and ")
		case strings.HasPrefix(rest, ","):
			rest = strings.TrimPrefix(rest, ",")
		default:
			return nil, fmt.Errorf("run id list %q: unexpected text %q after id %q", phrase, rest, ids[len(ids)-1])
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("run id list %q: no run ids", phrase)
	}
	return ids, nil
}

func startRunsOfFlowWaitingAtGate(ctx context.Context, tenant, runIDs, flow, gate string) error {
	w := localStepWorld(ctx)
	ids, err := parseStrictRunIDList(runIDs)
	if err != nil {
		return err
	}
	ps := w.pagingState()
	ps.seededRunIDs = append([]string(nil), ids...)
	w.setFlow(flow, w.gateFlow(gate))
	for _, runID := range ids {
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
		ps := w.pagingState()
		firstID := ""
		if len(ps.seededRunIDs) > 0 {
			firstID = ps.seededRunIDs[0]
		} else {
			all, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow})
			if err != nil {
				return fmt.Errorf("list runs to anchor cursor: %w", err)
			}
			if len(all) == 0 {
				return fmt.Errorf("no runs of flow %q to anchor a past-the-end cursor", flow)
			}
			firstID = all[0].Ref.RunID
		}
		valid := craftListCursorToken(tenant, 0)
		control, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow, Cursor: valid})
		if err != nil {
			return fmt.Errorf("control list at valid cursor: %w", err)
		}
		if len(control) != 1 || control[0].Ref.RunID != firstID {
			return fmt.Errorf("control list = %v, want one run with id %q", runIDsOf(control), firstID)
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
// It hand-builds the wire shape instead of calling the production encoder because
// the step plays a client forging a token; the control list step first uses an
// in-range crafted cursor so a drift in the production format fails there instead
// of letting the "past the last run" scenario pass for the wrong reason.
func craftListCursorToken(tenant string, pos int) string {
	raw, err := json.Marshal(listCursorWire{V: 1, Tenant: tenant, Pos: pos})
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}
