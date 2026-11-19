package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// runJSON is one run as the API returns it. It deliberately has no tenant
// field: the response is already scoped to the caller's tenant, and a tenant id
// in a body would be one more place for another tenant's record to leak.
type runJSON struct {
	ID        string     `json:"id"`
	State     string     `json:"state"`
	StartedAt time.Time  `json:"started_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

// runListJSON is the body of a list response. NextCursor is set when the
// control plane returned another page.
type runListJSON struct {
	Runs       []runJSON `json:"runs"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

func newRunJSON(status runtime.RunStatus) runJSON {
	return runJSON{
		ID:        status.Ref.RunID,
		State:     status.State,
		StartedAt: status.StartedAt.UTC(),
		ClosedAt:  status.ClosedAt,
	}
}

// requestTenant returns the tenant the authenticated request acts for. A
// principal without a valid tenant is unauthenticated, not tenantless data.
func requestTenant(ctx context.Context) (core.TenantID, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return "", core.NewError(core.CodeUnauthenticated, "the request has no tenant", nil)
	}
	return tenant, nil
}

// listRuns serves GET /v1/runs. The control plane filters by the context
// tenant; the handler filters again, so a control-plane bug cannot return
// another tenant's run (REQ-AUTH-002).
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenant, err := requestTenant(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := intQuery(r, "limit")
	if err != nil {
		writeError(w, err)
		return
	}
	statuses, next, err := s.opts.Runs.List(ctx, runtime.RunQuery{
		State:  r.URL.Query().Get("state"),
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	out := runListJSON{Runs: make([]runJSON, 0, len(statuses)), NextCursor: next}
	for _, status := range statuses {
		if status.Ref.TenantID != tenant {
			continue
		}
		out.Runs = append(out.Runs, newRunJSON(status))
	}
	writeJSON(w, http.StatusOK, out)
}

// getRun serves GET /v1/runs/{id}. A run of another tenant is answered as
// not_found by the control plane; a control-plane implementation that returned
// it anyway is still filtered here.
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenant, err := requestTenant(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, core.NewError(core.CodeInvalidArgument, "api: run id is empty", nil))
		return
	}
	status, err := s.opts.Runs.Describe(ctx, runtime.RunRef{RunID: id})
	if err != nil {
		writeError(w, err)
		return
	}
	if status.Ref.TenantID != tenant {
		writeError(w, core.NewError(core.CodeNotFound, "api: run not found", nil))
		return
	}
	writeJSON(w, http.StatusOK, newRunJSON(status))
}

// cancelRun serves POST /v1/runs/{id}/cancel. Cancellation is a side effect, so
// it runs the permission check first; a server without an authorizer refuses it
// as unsupported rather than allowing it unchecked (REQ-AUTH-004).
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenant, err := requestTenant(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	if s.opts.Canceller == nil || s.opts.Authorizer == nil {
		writeError(w, core.NewError(core.CodeUnsupported, "api: cancelling runs is not enabled", nil))
		return
	}
	principal, ok := principalFrom(ctx)
	if !ok {
		writeError(w, core.NewError(core.CodeUnauthenticated, "the request has no principal", nil))
		return
	}
	resource := authz.Resource{Tenant: tenant, Project: s.project(tenant)}
	if err := s.opts.Authorizer.Check(ctx, principal, authz.ActionRunCancel, resource); err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, core.NewError(core.CodeInvalidArgument, "api: run id is empty", nil))
		return
	}
	if err := s.opts.Canceller.Cancel(ctx, runtime.RunRef{RunID: id}, r.URL.Query().Get("reason")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// intQuery parses a non-negative integer query parameter. Absent means zero.
func intQuery(r *http.Request, name string) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, core.NewError(core.CodeInvalidArgument, "api: "+name+" must be a non-negative integer", nil)
	}
	return n, nil
}
