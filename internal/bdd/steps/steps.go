// Package steps holds the shared step library. Steps are thin: they translate
// glossary phrases into calls on the World's drivers and assert on results.
// Add a phrase only together with the scenario that needs it; keep phrases in
// the language of specs/glossary.md.
package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
)

type worldKey struct{}

// World is per-scenario state. Never share it between scenarios.
type World struct {
	Core        *coreWorld
	SQL         *sqlWorld
	Prompt      *promptWorld
	Secrets     *secretsWorld
	Credentials *credentialsWorld
	Expr        *exprWorld
	Schema      *schemaWorld
	Model       *modelWorld
	Agent       *agentWorld
	Flow        *flowWorld
	Machine     *machineWorld
	Host        *hostWorld
	Local       *localWorld
	Tool        *toolWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps.
func Initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{
			Core:    newCoreWorld(),
			Prompt:  newPromptWorld(),
			Expr:    &exprWorld{bindings: map[string]any{}},
			Schema:  newSchemaWorld(),
			Model:   newModelWorld(),
			Agent:   newAgentWorld(),
			Flow:    newFlowWorld(),
			Machine: newMachineWorld(),
			Host:    newHostWorld(),
			Local:   newLocalWorld(),
			Tool:    newToolWorld(),
		}), nil
	})
	registerCore(sc)
	registerSQL(sc)
	registerPrompt(sc)
	registerSecrets(sc)
	registerCredentials(sc)
	registerExpr(sc)
	registerSchema(sc)
	registerModel(sc)
	registerAgent(sc)
	registerMiddleware(sc)
	registerMiddlewareCalls(sc)
	registerFlowCompile(sc)
	registerFlowMachine(sc)
	registerFlowHost(sc)
	registerLocal(sc)
	registerTool(sc)
	registerShared(sc)
}

// registerShared holds phrases used by more than one domain. The SQL store and
// the Secrets port both report an open failure with the same phrase; one
// handler reads whichever domain opened in the scenario. The clock step is
// shared by the flow host and the model middleware: both worlds advance, and
// the one that is active settles.
func registerShared(sc *godog.ScenarioContext) {
	sc.Step(`^(\d+) (second|seconds|minute|minutes|hour|hours) pass(?:es)?$`, func(ctx context.Context, count int, unit string) error {
		var duration time.Duration
		switch strings.TrimSuffix(unit, "s") {
		case "second":
			duration = time.Duration(count) * time.Second
		case "minute":
			duration = time.Duration(count) * time.Minute
		case "hour":
			duration = time.Duration(count) * time.Hour
		}
		w := world(ctx)
		if w.Tool != nil && w.Tool.pending != nil {
			return w.Tool.advance(w, duration)
		}
		if w.Host != nil && w.Host.dc != nil {
			w.Core.clock.Advance(duration)
			w.Host.dc.advance(duration)
			w.Host.signal()
			return w.Host.settle()
		}
		if w.Model != nil && w.Model.call != nil {
			w.Model.call.rt.Advance(duration)
			return nil
		}
		w.Core.clock.Advance(duration)
		return nil
	})
	sc.Step(`^opening fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx)
		var err error
		if w.SQL != nil {
			err = w.SQL.openErr
		}
		if err == nil && w.Secrets != nil {
			err = w.Secrets.lastErr
		}
		if err == nil {
			return fmt.Errorf("opening succeeded, want %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s: %w", got, code, err)
		}
		return nil
	})
}
