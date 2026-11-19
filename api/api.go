package api

import (
	"context"
	"net/http"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/stream"
)

// Runs is the control-plane view of runs the API serves. runtime.Runtime fits
// it; the embedded runtime is provided by the composition root.
type Runs interface {
	// List returns the tenant's runs. The tenant comes from ctx, as in every
	// runtime call, never from the query.
	List(ctx context.Context, q runtime.RunQuery) ([]runtime.RunStatus, string, error)
	// Describe returns one run. A run of another tenant is not_found.
	Describe(ctx context.Context, ref runtime.RunRef) (runtime.RunStatus, error)
}

// Canceller cancels a run. runtime.Runtime fits it.
type Canceller interface {
	Cancel(ctx context.Context, ref runtime.RunRef, reason string) error
}

// Attachment is one attached client stream. *stream.Attachment fits it.
type Attachment interface {
	// Next returns the next frame, blocking until one is ready or ctx ends.
	Next(ctx context.Context) (stream.Frame, error)
	// Cursor is the position the client has reached.
	Cursor() stream.Cursor
	// Close detaches the client.
	Close() error
}

// Streams attaches to a run's native stream. *stream.Broker fits it.
type Streams interface {
	Attach(ctx context.Context, runID string, cursor stream.Cursor) (Attachment, error)
}

// Authorizer decides whether a principal may perform an action. *authz.Authorizer
// fits it. It is consulted only for side effects, such as cancelling a run; run
// reads are tenant-scoped and need no role.
type Authorizer interface {
	Check(ctx context.Context, principal authn.Principal, action authz.Action, resource authz.Resource) error
}

// Options configures a Server. Runs and Authenticator are required.
type Options struct {
	// Runs serves the run routes. Required.
	Runs Runs
	// Canceller cancels runs. When nil the cancel route answers unsupported.
	Canceller Canceller
	// Streams serves the native stream and the AG-UI adapter. When nil both
	// routes answer unsupported.
	Streams Streams
	// Authenticator resolves every request's principal. Required.
	Authenticator Authenticator
	// Authorizer gates cancellation. A cancel request without it is unsupported:
	// the API never performs a side effect that was not checked.
	Authorizer Authorizer
	// Project maps a tenant to the project scope an authorization check uses.
	// Until runs carry their project, the tenant is the only scope a request
	// has; nil means the tenant id itself.
	Project func(core.TenantID) string
}

// Server is the API's http.Handler. Build it once and mount it in the
// composition root; it holds no per-request state.
type Server struct {
	opts Options
	mux  *http.ServeMux
}

// New builds a Server for opts. It reports invalid_argument for a missing Runs
// or Authenticator, so a misconfigured server never starts open.
func New(opts Options) (*Server, error) {
	if opts.Runs == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "api: a run control plane is required", nil)
	}
	if opts.Authenticator == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "api: an authenticator is required", nil)
	}
	s := &Server{opts: opts, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /v1/runs", s.listRuns)
	s.mux.HandleFunc("GET /v1/runs/{id}", s.getRun)
	s.mux.HandleFunc("POST /v1/runs/{id}/cancel", s.cancelRun)
	s.mux.HandleFunc("GET /v1/runs/{id}/stream", s.attachStream)
	s.mux.HandleFunc("GET /agui", s.attachAGUI)
	return s, nil
}

// Handler returns the server as an http.Handler.
func (s *Server) Handler() http.Handler { return s }

// ServeHTTP authenticates the request and dispatches it. An authentication
// failure is answered with its code before any route runs.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, _, err := s.authenticate(r)
	if err != nil {
		writeError(w, err)
		return
	}
	s.mux.ServeHTTP(w, r.WithContext(ctx))
}

// project is the authorization scope of a tenant-scoped request.
func (s *Server) project(tenant core.TenantID) string {
	if s.opts.Project != nil {
		return s.opts.Project(tenant)
	}
	return string(tenant)
}
