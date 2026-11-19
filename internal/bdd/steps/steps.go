// Package steps holds the shared step library. Steps are thin: they translate
// glossary phrases into calls on the World's drivers and assert on results.
// Add a phrase only together with the scenario that needs it; keep phrases in
// the language of specs/glossary.md.
package steps

import (
	"context"

	"github.com/cucumber/godog"
)

type worldKey struct{}

// World is per-scenario state. Never share it between scenarios. Fields stay
// sorted alphabetically.
type World struct {
	Agent        *agentWorld
	Auth         *authWorld
	Blob         *blobWorld
	Bus          *busWorld
	Catalog      *catalogWorld
	Codegen      *codegenWorld
	Config       *configWorld
	Conversation *convWorld
	Core         *coreWorld
	Credentials  *credentialsWorld
	Eventlog     *eventlogWorld
	Expr         *exprWorld
	Flow         *flowWorld
	FlowBuilder  *builderWorld
	Host         *hostWorld
	Local        *localWorld
	Machine      *machineWorld
	Model        *modelWorld
	Node         *nodeWorld
	Prompt       *promptWorld
	Providers    *providerWorld
	Schema       *schemaWorld
	Secrets      *secretsWorld
	SQL          *sqlWorld
	Stream       *streamWorld
	Telemetry    *telemetryWorld
	Temporal     *temporalWorld
	Tool         *toolWorld
	Worker       *workerWorld
}

func world(ctx context.Context) *World { return ctx.Value(worldKey{}).(*World) }

// Initialize registers hooks and every domain's steps. It creates only the
// empty World; each register* fills its own field in its own Before hook.
func Initialize(sc *godog.ScenarioContext) {
	sharedFailures = newFailureRegistry()
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return context.WithValue(ctx, worldKey{}, &World{}), nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Config; w != nil {
			w.teardown()
		}
		return ctx, nil
	})
	// Sorted alphabetically, one register per line.
	registerAgent(sc)
	registerAuth(sc)
	registerBlob(sc)
	registerBlobUploads(sc)
	registerBus(sc)
	registerCatalog(sc)
	registerCodegen(sc)
	registerConfig(sc)
	registerConfigServe(sc)
	registerConversation(sc)
	registerCore(sc)
	registerCredentials(sc)
	registerEventlog(sc)
	registerExpr(sc)
	registerFlowBuilder(sc)
	registerFlowCompile(sc)
	registerFlowHost(sc)
	registerFlowLimits(sc)
	registerFlowMachine(sc)
	registerLocal(sc)
	registerMiddleware(sc)
	registerMiddlewareCalls(sc)
	registerModel(sc)
	registerModelRetention(sc)
	registerNode(sc)
	registerPrompt(sc)
	registerProviders(sc)
	registerSchema(sc)
	registerSecrets(sc)
	registerSharedSteps(sc)
	registerSQL(sc)
	registerStreamLive(sc)
	registerTelemetry(sc)
	registerTemporal(sc)
	registerTemporalLocalRunSteps(sc)
	registerTool(sc)
	registerWorker(sc)
}
