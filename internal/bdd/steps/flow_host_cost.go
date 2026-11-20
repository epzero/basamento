package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	hostpkg "github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
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
			w.addNode(repair, repairKind, "", "run.cost", "")
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
			// Observed via run.cost passed into the next handler after the tool step.
			accrual, ok := accruedCostBeforeRepairHandler(w, "repair")
			if !ok {
				return fmt.Errorf("node handler %q was never called", "repair")
			}
			if accrual != int64(want) {
				return fmt.Errorf("run cost before downstream handler %d, want %d", accrual, want)
			}
			_ = step
			return nil
		})

	sc.Step(`^the node "([^"]*)" failure reported a settled cost of (\d+) milli-points$`,
		func(ctx context.Context, node string, want int) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			if w.err == nil {
				return fmt.Errorf("run succeeded, want failure")
			}
			want64 := int64(want)
			if want64 == 0 {
				return fmt.Errorf("a zero cost cannot be probed")
			}
			// A failed node never completes, so run.cost never reflects its cost.
			// The machine's overflow check is the only black-box probe: prepend an
			// invisible ballast handler that accrues MaxInt64-N+delta; delta=0 must
			// still fail with the scenario error, delta=1 must hit cost_mpts overflow.
			return probeFailureCostMpts(w, node, want64, core.CodeOf(w.err))
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

func accruedCostBeforeRepairHandler(w *hostWorld, downstream string) (int64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, call := range w.handlerCalls {
		if call.node != downstream {
			continue
		}
		var asInt int64
		raw := strings.TrimSpace(string(call.input))
		if raw == "" {
			return 0, true
		}
		if err := json.Unmarshal(call.input, &asInt); err != nil {
			var asFloat float64
			if err2 := json.Unmarshal(call.input, &asFloat); err2 != nil {
				return 0, true
			}
			asInt = int64(asFloat)
		}
		return asInt, true
	}
	return 0, false
}

func probeFailureCostMpts(w *hostWorld, failingNode string, want int64, scenarioCode core.Code) error {
	w.mu.Lock()
	b := w.behaviors[failingNode]
	if b == nil {
		w.mu.Unlock()
		return fmt.Errorf("no handler behavior for %q", failingNode)
	}
	failOutput := append(json.RawMessage(nil), b.output...)
	failCost := b.costMpts
	var failErr error
	if b.err != nil {
		failErr = b.err
	}
	def := cloneFlowIR(w.flow)
	if def == nil {
		w.mu.Unlock()
		return fmt.Errorf("no flow to probe")
	}
	kind := def.Nodes[failingNode].Kind
	w.mu.Unlock()

	runProbe := func(delta int64) (core.Code, string, error) {
		probe := newProbeHost(def, kind, failingNode, failOutput, failCost, failErr, want, delta)
		defer probe.dc.abort()
		done := make(chan struct{})
		var runErr error
		ref := probe.catalog.register(probe.flow)
		go func() {
			_, runErr = probe.host.Run(probe.dc, ref, json.RawMessage(`{}`))
			close(done)
			probe.dc.markFinished()
		}()
		probe.dc.waitFinished()
		<-done
		code, msg, _ := coreErrorParts(runErr)
		return code, msg, runErr
	}
	code0, _, err0 := runProbe(0)
	if err0 == nil {
		return fmt.Errorf("delta=0 probe run succeeded, want failure")
	}
	if code0 != scenarioCode {
		return fmt.Errorf("delta=0 probe code %s, want scenario %s: %w", code0, scenarioCode, err0)
	}
	code1, msg1, err1 := runProbe(1)
	if err1 == nil {
		return fmt.Errorf("delta=1 probe run succeeded, want overflow failure")
	}
	if code1 != core.CodeInvalidArgument || !strings.Contains(msg1, "cost_mpts overflow") {
		return fmt.Errorf("delta=1 probe %s: %w, want invalid_argument with cost_mpts overflow", msg1, err1)
	}
	return nil
}

type probeHostWorld struct {
	host    *hostpkg.Host
	catalog *hostCatalog
	flow    *flow.IR
	dc      *hostFake
}

func newProbeHost(def *flow.IR, kind, failing string, failOut json.RawMessage, failCost int64, failErr error, target int64, delta int64) *probeHostWorld {
	probeDef := cloneFlowIR(def)
	ballast := "ballast"
	probeDef.Nodes[ballast] = flow.Node{Kind: kind, In: "", Out: ""}
	for i, e := range probeDef.Edges {
		if e.From == ir.Start {
			probeDef.Edges[i].From = ballast
			break
		}
	}
	probeDef.Edges = append([]flow.Edge{{From: ir.Start, To: ballast}}, probeDef.Edges...)

	cat := newHostCatalog()
	_ = cat.register(probeDef)
	dc := newHostFake()
	h := hostpkg.New(cat)
	h.RegisterHandler(kind, func(_ runtime.DurableContext, node flow.Node, _ json.RawMessage) (hostpkg.NodeResult, error) {
		switch node.Name {
		case ballast:
			cost := int64(math.MaxInt64) - target + delta
			return hostpkg.NodeResult{CostMpts: cost}, nil
		case failing:
			return hostpkg.NodeResult{Output: failOut, CostMpts: failCost}, failErr
		default:
			return hostpkg.NodeResult{}, fmt.Errorf("unexpected node %q", node.Name)
		}
	})
	return &probeHostWorld{host: h, catalog: cat, flow: probeDef, dc: dc}
}

func cloneFlowIR(def *flow.IR) *flow.IR {
	if def == nil {
		return nil
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return nil
	}
	var out flow.IR
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return &out
}

func coreErrorParts(err error) (core.Code, string, error) {
	if err == nil {
		return "", "", nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return pe.Code, pe.Message, err
	}
	return core.CodeOf(err), err.Error(), err
}
