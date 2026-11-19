// Package steps holds the shared step library. Steps are thin: they translate
// glossary phrases into calls on the World's drivers and assert on results.
// Add a phrase only together with the scenario that needs it; keep phrases in
// the language of specs/glossary.md.
package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
)

type worldKey struct{}

// World is per-scenario state. Never share it between scenarios.
type World struct {
	Core        *coreWorld
	SQL         *sqlWorld
	Prompt      *promptWorld
	Blob        *blobWorld
	Bus         *busWorld
	Secrets     *secretsWorld
	Config      *configWorld
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
	Codegen     *codegenWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps.
func Initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{
			Core:    newCoreWorld(),
			SQL:     newSQLWorld(),
			Prompt:  newPromptWorld(),
			Blob:    newBlobWorld(),
			Bus:     newBusWorld(),
			Secrets: newSecretsWorld(),
			Config:  newConfigWorld(),
			Expr:    &exprWorld{bindings: map[string]any{}},
			Schema:  newSchemaWorld(),
			Model:   newModelWorld(),
			Agent:   newAgentWorld(),
			Flow:    newFlowWorld(),
			Machine: newMachineWorld(),
			Host:    newHostWorld(),
			Local:   newLocalWorld(),
			Tool:    newToolWorld(),
			Codegen: newCodegenWorld(),
		}), nil
	})
	registerCore(sc)
	registerSQL(sc)
	registerPrompt(sc)
	registerBlob(sc)
	registerBus(sc)
	registerSecrets(sc)
	registerConfig(sc)
	registerConfigServe(sc)
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
	registerCodegen(sc)
	registerOpeningSteps(sc)
	registerSharedSteps(sc)
	registerClockSteps(sc)
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		world(ctx).Config.teardown()
		return ctx, nil
	})
}

func registerSharedSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the call fails with code "([^"]*)"$`, assertCallFailsWithCode)
}

func assertCallFailsWithCode(ctx context.Context, code string) error {
	w := world(ctx)
	if w.SQL != nil {
		err := w.SQL.lastErr
		if err == nil {
			err = w.SQL.lastTxErr
		}
		if err != nil {
			if string(core.CodeOf(err)) != code {
				return fmt.Errorf("got code %s, want %s", core.CodeOf(err), code)
			}
			return nil
		}
	}
	if w.Blob != nil && (w.Blob.lastErr != nil || w.Blob.openErr != nil) {
		return assertBlobCallCode(ctx, code)
	}
	return fmt.Errorf("call succeeded, want %s", code)
}

// registerClockSteps holds the shared "N <unit> pass" step. A duration belongs
// to whichever world is active: a pending tool call, a flow host run, a model
// middleware call, or the core clock. The pattern names its units so it never
// swallows a longer phrase such as "a run of "timer" starts and 29 minutes pass".
func registerClockSteps(sc *godog.ScenarioContext) {
	sc.Step(`^((?:\d+ (?:milliseconds?|seconds?|minutes?|hours?|days?)\s*)+)pass(?:es)?$`, func(ctx context.Context, phrase string) error {
		duration, err := parseDurationPass(strings.TrimSpace(phrase))
		if err != nil {
			return err
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
		// A component server times its shutdown drain from the same frozen
		// clock; firing its timer here is what "the seconds pass" means.
		if w.Config != nil && w.Config.after != nil {
			select {
			case w.Config.after <- w.Core.clock.Now():
			default:
			}
		}
		return nil
	})
}
