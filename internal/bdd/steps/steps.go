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
	Schema      *schemaWorld
	Expr        *exprWorld
	SQL         *sqlWorld
	Prompt      *promptWorld
	Telemetry   *telemetryWorld
	Blob        *blobWorld
	Bus         *busWorld
	Secrets     *secretsWorld
	Credentials *credentialsWorld
	Flow        *flowWorld
	FlowBuilder *builderWorld
	Catalog     *catalogWorld
	Model       *modelWorld
	Machine     *machineWorld
	Host        *hostWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps.
func Initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{
			Core:        newCoreWorld(),
			Schema:      newSchemaWorld(),
			Expr:        &exprWorld{bindings: map[string]any{}},
			SQL:         newSQLWorld(),
			Prompt:      newPromptWorld(),
			Telemetry:   newTelemetryWorld(),
			Blob:        newBlobWorld(),
			Secrets:     newSecretsWorld(),
			Flow:        newFlowWorld(),
			FlowBuilder: newBuilderWorld(),
			Catalog:     newCatalogWorld(),
			Model:       newModelWorld(),
			Machine:     newMachineWorld(),
			Host:        newHostWorld(),
		}), nil
	})
	registerCore(sc)
	registerSchema(sc)
	registerExpr(sc)
	registerSQL(sc)
	registerPrompt(sc)
	registerBlob(sc)
	registerTelemetry(sc)
	registerFlowCompile(sc)
	registerFlowLimits(sc)
	registerFlowBuilder(sc)
	registerFlowMachine(sc)
	registerFlowHost(sc)
	registerBus(sc)
	registerSharedSteps(sc)
	registerBlobUploads(sc)
	registerSecrets(sc)
	registerOpeningSteps(sc)
	registerCredentials(sc)
	registerCatalog(sc)
	registerModel(sc)
	registerModelRetention(sc)
	registerMiddleware(sc)
	registerMiddlewareCalls(sc)
}

func registerSharedSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the call fails with code "([^"]*)"$`, assertCallFailsWithCode)
	sc.Step(`^(.+?) pass(?:es)?$`, advanceClockByDurationPhrase)
	sc.Step(`^the put (succeeds|fails with code "[^"]+")$`, matchPutOutcomeShared)
	registerSharedRunStartStep(sc)
	registerSharedRunOutcomeSteps(sc)
}

// matchPutOutcomeShared owns the put phrase for every capability, because godog
// has one step namespace and both the bus and the credentials scenarios say
// "the put fails with code ...". The KV world is created only for bus scenarios
// (registerBus) and the credentials world only for credentials ones, so the
// worlds tell the two apart without reading the text.
func matchPutOutcomeShared(ctx context.Context, tail string) error {
	if world(ctx).Bus != nil {
		return matchPutOutcome(ctx, tail)
	}
	if strings.HasPrefix(tail, `fails with code "`) {
		code := strings.TrimSuffix(strings.TrimPrefix(tail, `fails with code "`), `"`)
		return credentialsCode(credentialsW(ctx).putErr, code)
	}
	return fmt.Errorf("unknown put outcome %q", tail)
}

func advanceClockByDurationPhrase(ctx context.Context, elapsed string) error {
	d, err := parseElapsed(elapsed)
	if err != nil {
		return err
	}
	world(ctx).Core.clock.Advance(d)
	if sink, ok := any(world(ctx).Host).(timeSink); ok {
		return sink.timePassed(ctx, d)
	}
	return nil
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
	if w.Bus != nil && w.Bus.lastErr != nil {
		if string(core.CodeOf(w.Bus.lastErr)) != code {
			return fmt.Errorf("got code %s, want %s", core.CodeOf(w.Bus.lastErr), code)
		}
		return nil
	}
	return fmt.Errorf("call succeeded, want %s", code)
}
