package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

// The REQ-FLOW-046 scenarios build a flow whose node count, edge count,
// expression size or canonical size sits at a limit or one above it, then
// compile it.

// limitUnits is the unit each measure of REQ-FLOW-046 is counted in.
var limitUnits = map[string]string{
	"node count":      "nodes",
	"edge count":      "edges",
	"expression size": "bytes",
	"definition size": "bytes",
}

// limitFlow builds a valid flow whose named measure is size: the shape the
// size-limit scenarios describe, at a maximum or one above it.
func (w *flowWorld) limitFlow(measure string, size int) error {
	switch measure {
	case "node count":
		if size < 1 {
			return fmt.Errorf("a flow needs at least one node")
		}
		w.chainFlow(size)
	case "edge count":
		// The node maximum holds the edges: a chain of MaxNodes nodes has
		// MaxNodes+1 edges, and the rest go to FAIL.
		w.chainFlow(ir.MaxNodes)
		padEdges(w.ir, size)
	case "expression size":
		w.sizedFlow(expressionOf(size))
	case "definition size":
		w.sizedFlow("")
		canonical, err := padDefinition(w.ir, size)
		if err != nil {
			return err
		}
		if len(canonical) != size {
			return fmt.Errorf("padded the definition to %d bytes, want %d", len(canonical), size)
		}
	default:
		return fmt.Errorf("unknown measure %q", measure)
	}
	return nil
}

// chainFlow declares a line of count task nodes from START to FAIL.
func (w *flowWorld) chainFlow(count int) {
	w.ir = newIR("chain")
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("n%05d", i)
		w.addNode(ids[i])
	}
	w.addEdge(ir.Start, ids[0], "")
	for i := 0; i+1 < len(ids); i++ {
		w.addEdge(ids[i], ids[i+1], "")
	}
	w.addEdge(ids[len(ids)-1], ir.Fail, "")
}

// sizedFlow declares the one-node flow whose edge condition is the expression given.
func (w *flowWorld) sizedFlow(when string) {
	w.ir = newIR("sized")
	w.addEdge(ir.Start, "check", when)
	w.addEdge("check", ir.Fail, "")
}

// padEdges adds edges to FAIL, cycling through the nodes in id order, until the
// flow carries total edges. Routing order is preserved and the graph stays
// acyclic, so the flow differs from a valid one only in its edge count.
func padEdges(f *flow.IR, total int) {
	ids := make([]string, 0, len(f.Nodes))
	for id := range f.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for i := 0; len(f.Edges) < total; i++ {
		f.Edges = append(f.Edges, flow.Edge{From: ids[i%len(ids)], To: ir.Fail})
	}
}

// expressionOf returns a CEL expression of exactly n bytes that type-checks:
// "true" joined by " || true", padded with trailing spaces. The limit is a
// property of the bytes, not of the parse tree.
func expressionOf(n int) string {
	const unit = " || true"
	s := "true"
	for len(s)+len(unit) <= n {
		s += unit
	}
	if n < len(s) {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

// padDefinition grows f's defaults until its canonical JSON is exactly size
// bytes and returns those bytes. A JSON string adds one canonical byte per
// character, so the loop converges in two passes whatever the key's own
// overhead is.
func padDefinition(f *flow.IR, size int) ([]byte, error) {
	if f.Defaults == nil {
		f.Defaults = map[string]json.RawMessage{"padding": nil}
	}
	pad := 0
	for i := 0; i < 5; i++ {
		f.Defaults["padding"] = json.RawMessage(`"` + strings.Repeat("a", pad) + `"`)
		canonical, err := ir.Canonical(f)
		if err != nil {
			return nil, err
		}
		delta := size - len(canonical)
		if delta == 0 {
			return canonical, nil
		}
		if pad+delta < 0 {
			return nil, fmt.Errorf("cannot pad a definition to %d bytes", size)
		}
		pad += delta
	}
	return nil, fmt.Errorf("could not pad a definition to %d bytes", size)
}

func registerFlowLimits(sc *godog.ScenarioContext) {
	sc.Step(`^a flow whose (node count|edge count|expression size|definition size) is (\d+) (nodes|edges|bytes)$`,
		func(ctx context.Context, measure string, size int, unit string) error {
			if want := limitUnits[measure]; unit != want {
				return fmt.Errorf("%s is counted in %s, not %s", measure, want, unit)
			}
			return world(ctx).Flow.limitFlow(measure, size)
		})

	sc.Step(`^compilation fails with code "([^"]*)" naming node "([^"]*)", the field "([^"]*)" and the maximum (\d+)$`,
		func(ctx context.Context, code, name, field, maximum string) error {
			platform, err := world(ctx).Flow.failure(code)
			if err != nil {
				return err
			}
			for _, want := range []string{fmt.Sprintf("node %q", name), field, maximum} {
				if !strings.Contains(platform.Message, want) {
					return fmt.Errorf("message %q does not contain %q", platform.Message, want)
				}
			}
			return nil
		})

	sc.Step(`^compilation fails with code "([^"]*)" naming the (node count|edge count|expression size|definition size) limit of (\d+) (nodes|edges|bytes)$`,
		func(ctx context.Context, code, measure, maximum, unit string) error {
			platform, err := world(ctx).Flow.failure(code)
			if err != nil {
				return err
			}
			if want := fmt.Sprintf("%s limit of %s %s", measure, maximum, unit); !strings.Contains(platform.Message, want) {
				return fmt.Errorf("message %q does not contain %q", platform.Message, want)
			}
			return nil
		})

}
