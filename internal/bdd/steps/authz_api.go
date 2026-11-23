package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/credentials"
)

type authzRunReq struct {
	Project string
	RunID   string
}

type authzProjectReq struct {
	Project string
}

type authzCredResolveReq struct {
	Project string
	Name    string
}

type authzTestAPI struct {
	w *authzWorld

	runs           map[string]map[string]string // project -> runID -> state
	gateProject    string
	messageProject string
	startedRunID   string

	credStore *credentials.Store
}

func newAuthzTestAPI(w *authzWorld) *authzTestAPI {
	return &authzTestAPI{
		w: w,
		runs: map[string]map[string]string{
			"p1": {"r1": "active"},
			"p2": {"r2": "active"},
		},
	}
}

func (w *authzWorld) seedCredentialNames(ctx context.Context) error {
	sw := secretsW(ctx)
	sw.sim.hold(authzCredMasterKey)
	sec := &bddValueSecrets{reg: sw.sim, key: authzCredMasterKey}
	back := credentials.NewSQLBackend(w.db)
	store := credentials.New(back, w.auditWrap, sec, w.clock)
	if w.api == nil {
		w.api = newAuthzTestAPI(w)
	}
	w.api.credStore = store
	w.credSecret = []byte("secret-value")
	tctx := w.tenantCtx(w.tenant)
	if err := store.Put(tctx, "key", w.credSecret); err != nil {
		return err
	}
	return nil
}

func (a *authzTestAPI) goodDeclarations() []authz.Declaration {
	return a.baseDeclarations()
}

func (a *authzTestAPI) declarations() []authz.Declaration {
	switch a.w.rpcMode {
	case "no-action":
		return append(a.baseDeclarations(), authz.Declaration{
			Procedure: "bad.rpc",
			Action:    "",
			Resolve: func(context.Context, any) (authz.Resource, error) {
				return authz.Resource{}, nil
			},
		})
	case "no-resolver":
		return append(a.baseDeclarations(), authz.Declaration{
			Procedure: "bad.rpc",
			Action:    "run.get",
			Resolve:   nil,
		})
	}
	return a.baseDeclarations()
}

func (a *authzTestAPI) baseDeclarations() []authz.Declaration {
	return []authz.Declaration{
		{
			Procedure: "run.list",
			Action:    "run.list",
			Resolve: func(_ context.Context, request any) (authz.Resource, error) {
				req := request.(authzProjectReq)
				return authz.Resource{Project: req.Project, Kind: "project", ID: req.Project}, nil
			},
		},
		{
			Procedure: "run.start",
			Action:    "run.start",
			Resolve: func(_ context.Context, request any) (authz.Resource, error) {
				req := request.(authzProjectReq)
				return authz.Resource{Project: req.Project, Kind: "run", ID: "new"}, nil
			},
		},
		{
			Procedure: "run.cancel",
			Action:    "run.cancel",
			Resolve: func(_ context.Context, request any) (authz.Resource, error) {
				req := request.(authzRunReq)
				res := authz.Resource{Project: req.Project, Kind: "run", ID: req.RunID}
				if r, ok := a.w.resources["run:"+req.RunID]; ok {
					res.Session = r.Session
				}
				return res, nil
			},
		},
		{
			Procedure: "gate.resolve",
			Action:    "gate.resolve",
			Resolve: func(_ context.Context, request any) (authz.Resource, error) {
				req := request.(authzProjectReq)
				return authz.Resource{Project: req.Project, Kind: "gate", ID: "g1"}, nil
			},
		},
		{
			Procedure: "conversation.send",
			Action:    "conversation.send",
			Resolve: func(_ context.Context, request any) (authz.Resource, error) {
				req := request.(authzProjectReq)
				return authz.Resource{Project: req.Project, Kind: "conversation", ID: "c1"}, nil
			},
		},
		{
			Procedure: "credential.names",
			Action:    "credential.names",
			Resolve: func(_ context.Context, request any) (authz.Resource, error) {
				req := request.(authzProjectReq)
				return authz.Resource{Project: req.Project, Kind: "project", ID: req.Project}, nil
			},
		},
		{
			Procedure: "credential.resolve",
			Action:    "credential.resolve",
			Resolve: func(_ context.Context, request any) (authz.Resource, error) {
				req := request.(authzCredResolveReq)
				name := strings.TrimPrefix(req.Name, "cred://")
				return authz.Resource{Project: req.Project, Kind: "credential name", ID: name}, nil
			},
		},
		{
			Procedure: "binding.grant",
			Action:    "binding.grant",
			Resolve: func(_ context.Context, request any) (authz.Resource, error) {
				req := request.(authzProjectReq)
				return authz.Resource{Project: req.Project, Kind: "project", ID: req.Project}, nil
			},
		},
	}
}

func (a *authzTestAPI) invoke(ctx context.Context, who, proc string, req any) error {
	g := a.w.guard
	if g == nil {
		if a.w.guardWiringErr != nil && a.w.probeGuard != nil {
			g = a.w.probeGuard
		} else {
			return errBDDGuardNotWired
		}
	}
	p := a.w.principal(who, "")
	return g.Authorize(ctx, p, proc, req)
}

func (a *authzTestAPI) listRuns(ctx context.Context, who, project string) error {
	err := a.invoke(ctx, who, "run.list", authzProjectReq{Project: project})
	a.w.recordErr(err)
	if err != nil {
		return nil
	}
	a.w.lastList = sortedRunIDs(a.runs[project])
	return nil
}

func (a *authzTestAPI) startRun(ctx context.Context, who, project string) error {
	err := a.invoke(ctx, who, "run.start", authzProjectReq{Project: project})
	a.w.recordErr(err)
	if err != nil {
		return nil
	}
	if a.runs[project] == nil {
		a.runs[project] = map[string]string{}
	}
	const newID = "r-new"
	a.runs[project][newID] = "active"
	a.startedRunID = newID
	return nil
}

func (a *authzTestAPI) cancelRun(ctx context.Context, who, project, runID string) error {
	if runID == "" {
		runID = "r1"
	}
	err := a.invoke(ctx, who, "run.cancel", authzRunReq{Project: project, RunID: runID})
	a.w.recordErr(err)
	if err != nil {
		return nil
	}
	if a.runs[project] == nil {
		a.runs[project] = map[string]string{}
	}
	a.runs[project][runID] = "cancelled"
	return nil
}

func (a *authzTestAPI) resolveGate(ctx context.Context, who, project string) error {
	err := a.invoke(ctx, who, "gate.resolve", authzProjectReq{Project: project})
	a.w.recordErr(err)
	if err == nil {
		a.gateProject = project
	}
	return nil
}

func (a *authzTestAPI) sendMessage(ctx context.Context, who, project string) error {
	err := a.invoke(ctx, who, "conversation.send", authzProjectReq{Project: project})
	a.w.recordErr(err)
	if err == nil {
		a.messageProject = project
	}
	return nil
}

func (a *authzTestAPI) listCredNames(ctx context.Context, who, project string) error {
	err := a.invoke(ctx, who, "credential.names", authzProjectReq{Project: project})
	a.w.recordErr(err)
	if err != nil {
		return nil
	}
	tctx := a.w.tenantCtx(a.w.tenant)
	recs, err := credentials.NewSQLBackend(a.w.db).List(tctx)
	if err != nil {
		a.w.recordErr(err)
		return nil
	}
	names := make([]string, 0, len(recs))
	for _, r := range recs {
		names = append(names, r.Name)
	}
	a.w.lastNames = names
	body, err := json.Marshal(names)
	if err != nil {
		return err
	}
	a.w.lastNamesJSON = body
	return nil
}

func (a *authzTestAPI) resolveCred(ctx context.Context, who, project, raw string) error {
	err := a.invoke(ctx, who, "credential.resolve", authzCredResolveReq{Project: project, Name: raw})
	if err != nil {
		a.w.recordErr(err)
		a.w.lastResolveBody = []byte("null")
		return nil
	}
	tctx := a.w.tenantCtx(a.w.tenant)
	val, err := a.credStore.Resolve(tctx, raw)
	a.w.lastResolveBody = []byte("null")
	if err != nil {
		a.w.recordErr(err)
		return nil
	}
	a.w.credSink = append([]byte(nil), val...)
	a.w.recordErr(nil)
	return nil
}

func (a *authzTestAPI) attemptCheck(ctx context.Context, who, action string, res authz.Resource) error {
	p := a.w.principal(who, "")
	err := a.w.auth.Check(ctx, p, action, res)
	a.w.recordErr(err)
	return nil
}

func crossTenantCheck(ctx context.Context, tenant, who, project string) error {
	w := authzW(ctx)
	p := w.principal(who, "")
	if p.Tenant == core.TenantID(tenant) {
		return fmt.Errorf("principal tenant matches check tenant")
	}
	tctx := w.tenantCtx(tenant)
	res := authz.Resource{Project: project, Kind: "run", ID: "r1"}
	return w.api.attemptCheck(tctx, who, "run.cancel", res)
}
