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
	Local       *localWorld
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
			Local:       newLocalWorld(),
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
	registerLocal(sc)
	registerModelRetention(sc)
	registerMiddleware(sc)
	registerMiddlewareCalls(sc)
}

// durationPassStepPattern is the shared "N … pass" step. It is anchored on a
// duration phrase parseElapsed accepts so godog's single step namespace is not
// stolen from capabilities that own sentences ending in "pass" (for example a
// runtime run start or a worker grace period). Models middleware scenarios say
// "1 more second passes"; advanceClockByDurationPhrase strips the word "more".
const durationPassStepPattern = `^(-?\d+ (?:more )?[a-z]+(?: \d+ [a-z]+)*) pass(?:es)?$`

func registerSharedSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the call fails with code "([^"]*)"$`, assertCallFailsWithCode)
	sc.Step(durationPassStepPattern, advanceClockByDurationPhrase)
	sc.Step(`^the put (succeeds|fails with code "[^"]+")$`, matchPutOutcomeShared)
	registerSharedRunStartStep(sc)
	registerSharedRunOutcomeSteps(sc)
	registerSharedResolutionFailStep(sc)
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
	// The middleware scenarios say "1 more second passes" to step the call's
	// virtual runtime; the word carries no duration of its own.
	elapsed = strings.Replace(elapsed, " more ", " ", 1)
	d, err := parseElapsed(elapsed)
	if err != nil {
		return err
	}
	w := world(ctx)
	if cw := w.Model.call; cw != nil {
		// The call runs on its own virtual runtime over the scenario clock, so
		// advancing it also fires the call's timers and wakes its goroutine.
		cw.rt.Advance(d)
	} else {
		w.Core.clock.Advance(d)
	}
	if local := w.Local; local != nil {
		if err := local.timePassed(ctx, d); err != nil {
			return err
		}
	}
	if sink, ok := any(w.Host).(timeSink); ok {
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
