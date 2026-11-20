package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	hostpkg "github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
)

func registerFlowHostCost(sc *godog.ScenarioContext) {
	sc.Step(`^a flow that runs node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\}) before node "([^"]*)" of kind "([^"]*)" with a guard of (\d+) milli-points$`,
		func(ctx context.Context, work, workKind, workSpec, repair, repairKind string, guard int) error {
			w := world(ctx).Host
			w.newFlow("cost-tool", 1)
			w.stateSchema(hostObjectSchema(map[string]string{
				"ok":    `{"type":"boolean"}`,
				"fixed": `{"type":"boolean"}`,
			}))
			w.addNode(work, workKind, workSpec, "", "")
			w.addNode("stamp_ok", "set", `{"ok":true}`, "", "")
			w.addNode(repair, repairKind, "", "", "")
			w.addNode("stamp_fixed", "set", `{"fixed":true}`, "", "")
			def := w.ensureFlow()
			n := def.Nodes[repair]
			n.Guard = &flow.Guard{CostMpts: int64(guard)}
			def.Nodes[repair] = n
			w.addEdge(ir.Start, work, "")
			w.addEdge(work, "stamp_ok", "")
			w.addEdge("stamp_ok", repair, "")
			w.addEdge(repair, "stamp_fixed", "")
			w.registerHandledKind(repairKind)
			w.behavior(repair)
			return nil
		})

	sc.Step(`^the node handler of "([^"]*)" returns (\{.*\}) with a settled cost of (\d+) milli-points and the error "([^"]*)"$`,
		func(ctx context.Context, node, output string, cost int, code string) error {
			b := world(ctx).Host.behavior(node)
			if err := json.Unmarshal([]byte(output), &b.output); err != nil {
				return fmt.Errorf("handler output %q: %w", output, err)
			}
			b.costMpts = int64(cost)
			world(ctx).Host.handlerFails(node, core.NewError(core.Code(code), "handler failed", nil))
			return nil
		})

	sc.Step(`^the step "([^"]*)" contributed (\d+) to the run\'s settled cost$`,
		func(ctx context.Context, step string, want int) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			if w.err != nil {
				return fmt.Errorf("run failed: %w", w.err)
			}
			w.dc.mu.Lock()
			total := w.dc.runSettledCost
			w.dc.mu.Unlock()
			if want != 0 {
				return fmt.Errorf("tool steps cannot contribute non-zero cost")
			}
			for _, call := range w.dc.callLog() {
				if call.kind != "step" || call.name != step {
					continue
				}
				if cost := w.dc.stepCosts[step]; cost != 0 {
					return fmt.Errorf("step %q reported settled cost %d, want 0", step, cost)
				}
			}
			if total == 0 {
				return fmt.Errorf("run settled cost is 0, want handler-reported total from the host")
			}
			return nil
		})

	sc.Step(`^the node "([^"]*)" failure reported a settled cost of (\d+) milli-points$`,
		func(ctx context.Context, node string, want int) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			w.dc.mu.Lock()
			got, ok := w.dc.nodeFailureCost[node]
			w.dc.mu.Unlock()
			if !ok {
				return fmt.Errorf("no recorded failure cost for node %q", node)
			}
			if got != int64(want) {
				return fmt.Errorf("node %q failure settled cost %d milli-points, want %d", node, got, want)
			}
			return nil
		})

	sc.Step(`^a flow whose node "([^"]*)" of kind "map" fans out over (\d+) items$`,
		func(ctx context.Context, mapNode string, count int) error {
			w := world(ctx).Host
			w.newFlow("fanout", 1)
			w.stateSchema(hostObjectSchema(map[string]string{"items": `{"type":"array"}`}))
			items := make([]int, count)
			for i := range items {
				items[i] = i
			}
			payload, _ := json.Marshal(map[string]any{"items": items})
			w.addNode("fill", "set", string(payload), "", "")
			w.addNode(mapNode, "map", `{"over":"state.items","node":"work"}`, "", "")
			w.addNode("work", "tool", `{"tool":"w1"}`, "", "")
			w.addEdge(ir.Start, "fill", "")
			w.addEdge("fill", mapNode, "")
			w.addEdge(mapNode, "work", "")
			return nil
		})

	sc.Step(`^a flow host built with WithMaxFanout\((\d+)\)$`, func(ctx context.Context, limit int) error {
		world(ctx).Host.rebuildHost(hostpkg.WithMaxFanout(limit))
		return nil
	})

	sc.Step(`^the run fails with code "([^"]+)" naming node "([^"]*)", the item count (\d+) and the fan-out limit (\d+)$`,
		func(ctx context.Context, code, node string, count, limit int) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			gotCode, message, ok := w.failure()
			if !ok {
				return fmt.Errorf("run did not fail")
			}
			if gotCode != code {
				return fmt.Errorf("fail code %q, want %q", gotCode, code)
			}
			if !strings.Contains(message, fmt.Sprintf("%q", node)) {
				return fmt.Errorf("message %q does not name node %q", message, node)
			}
			if !strings.Contains(message, strconv.Itoa(count)) {
				return fmt.Errorf("message %q does not contain item count %d", message, count)
			}
			if !strings.Contains(message, strconv.Itoa(limit)) {
				return fmt.Errorf("message %q does not contain fan-out limit %d", message, limit)
			}
			return nil
		})
}
