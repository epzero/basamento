package inspector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
)

// Authenticator turns a request into the principal it acts for. The API's
// api.AuthenticatorFunc fits it. The inspector reads the tenant from that
// principal, never from the request (REQ-INSP-002).
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (authn.Principal, error)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(ctx context.Context, r *http.Request) (authn.Principal, error)

// Authenticate calls f.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, r *http.Request) (authn.Principal, error) {
	return f(ctx, r)
}

// Options configures a Handler. Source and Authenticator are required.
type Options struct {
	// Source is the tenant-scoped read model. Required.
	Source Source
	// Authenticator resolves a request's principal. Required.
	Authenticator Authenticator
	// Prefix is where the handler is mounted, such as "/inspector". Links
	// and form actions are absolute under it. Empty mounts at the root. Mount
	// the handler with http.StripPrefix(Prefix, handler), so its routes are
	// matched below the prefix.
	Prefix string
}

// Handler is the inspector's http.Handler. Build it once and mount it; it holds
// no per-request state.
type Handler struct {
	opts      Options
	prefix    string
	mux       *http.ServeMux
	templates map[string]*template.Template
}

// New builds a Handler for opts. It reports invalid_argument for a missing
// Source or Authenticator, so a misconfigured inspector never starts open.
func New(opts Options) (*Handler, error) {
	if opts.Source == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "inspector: a source is required", nil)
	}
	if opts.Authenticator == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "inspector: an authenticator is required", nil)
	}
	h := &Handler{opts: opts, prefix: normalisePrefix(opts.Prefix), mux: http.NewServeMux()}
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	h.templates = tmpl

	h.mux.HandleFunc("GET /{$}", h.list)
	h.mux.HandleFunc("GET /runs/{id}", h.detail)
	h.mux.HandleFunc("GET /runs/{id}/live", h.live)
	h.mux.HandleFunc("GET /runs/{id}/recording", h.recording)
	h.mux.HandleFunc("POST /runs/{id}/gates/{gate}", h.resolve)
	assets, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return nil, fmt.Errorf("inspector: embedded assets: %w", err)
	}
	h.mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	return h, nil
}

// Handler returns the inspector as an http.Handler.
func (h *Handler) Handler() http.Handler { return h }

// ServeHTTP serves embedded assets without a principal, then authenticates
// every other request and puts the principal on the context. Assets carry no
// tenant data; everything else requires a principal.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/assets/") {
		h.mux.ServeHTTP(w, r)
		return
	}
	ctx := r.Context()
	principal, err := h.opts.Authenticator.Authenticate(ctx, r)
	if err != nil {
		h.denied(w, http.StatusUnauthorized, core.CodeOf(err))
		return
	}
	tenant, ok := core.TenantFrom(core.WithTenant(ctx, principal.Tenant))
	if !ok {
		h.denied(w, http.StatusUnauthorized, core.CodeUnauthenticated)
		return
	}
	ctx = withPrincipal(core.WithTenant(ctx, tenant), principal)
	h.mux.ServeHTTP(w, r.WithContext(ctx))
}

// principalKey is the context key of the request's principal.
type principalKey struct{}

func withPrincipal(ctx context.Context, p authn.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func principalFrom(ctx context.Context) (authn.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(authn.Principal)
	return p, ok
}

// list renders the runs the principal may see (REQ-INSP-001). It filters by
// tenant again so a Source bug cannot list another tenant's run.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := principalFrom(ctx)
	if !ok {
		h.denied(w, http.StatusUnauthorized, core.CodeUnauthenticated)
		return
	}
	runs, err := h.opts.Source.List(ctx, p)
	if err != nil {
		h.sourceError(w, err)
		return
	}
	visible := make([]RunSummary, 0, len(runs))
	for _, run := range runs {
		if run.Tenant == p.Tenant {
			visible = append(visible, run)
		}
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].ID < visible[j].ID })
	body, err := json.Marshal(listJSON{Runs: visible})
	if err != nil {
		h.denied(w, http.StatusInternalServerError, core.CodeInternal)
		return
	}
	h.render(w, http.StatusOK, "list.html", listView{Prefix: h.prefix, Runs: visible, JSON: template.JS(body)})
}

// detail renders one run's stored events, open gates and recording
// (REQ-INSP-004, REQ-INSP-005, REQ-INSP-008, REQ-INSP-010).
func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	h.renderRun(w, r, http.StatusOK, nil)
}

// live answers the hydration endpoint the page polls: the run's state, open
// gates and recording, plus the events after the last sequence the page has.
// It lets an open run gain events, gates and a failed state while it is shown
// (REQ-INSP-003, REQ-INSP-005, REQ-INSP-008).
func (h *Handler) live(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := principalFrom(ctx)
	if !ok {
		writeError(w, http.StatusUnauthorized, core.CodeUnauthenticated)
		return
	}
	after, err := int64Query(r, "after")
	if err != nil {
		writeError(w, http.StatusBadRequest, core.CodeInvalidArgument)
		return
	}
	run, err := h.run(ctx, p, r.PathValue("id"))
	if err != nil {
		h.sourceError(w, err)
		return
	}
	events, err := h.opts.Source.Events(ctx, p, run.ID, after)
	if err != nil {
		h.sourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, liveJSON{Run: publicRun(run), Events: ordered(events)})
}

// recording returns the ordered frames of a run's rrweb recording for the
// player. None is recording_missing; one that cannot be loaded is a 502.
func (h *Handler) recording(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := principalFrom(ctx)
	if !ok {
		writeError(w, http.StatusUnauthorized, core.CodeUnauthenticated)
		return
	}
	run, err := h.run(ctx, p, r.PathValue("id"))
	if err != nil {
		h.sourceError(w, err)
		return
	}
	switch run.Recording.State() {
	case "none":
		writeError(w, http.StatusNotFound, core.CodeRecordingMissing)
	case "error":
		writeError(w, http.StatusBadGateway, core.CodeInternal)
	default:
		writeJSON(w, http.StatusOK, recordingJSON{Frames: run.Recording.Frames})
	}
}

// resolve submits a gate payload. A rejected payload is explained and the gate
// stays open; an accepted one returns the person to the run (REQ-INSP-006,
// REQ-INSP-007).
func (h *Handler) resolve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := principalFrom(ctx)
	if !ok {
		h.denied(w, http.StatusUnauthorized, core.CodeUnauthenticated)
		return
	}
	runID := r.PathValue("id")
	payload := json.RawMessage(r.FormValue("payload"))
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	err := h.opts.Source.Resolve(ctx, p, runID, r.PathValue("gate"), payload)
	if err == nil {
		http.Redirect(w, r, h.prefix+"/runs/"+url.PathEscape(runID), http.StatusSeeOther)
		return
	}
	if core.CodeOf(err) == core.CodeInvalidArgument {
		h.renderRun(w, r, http.StatusUnprocessableEntity, &Rejection{Code: string(core.CodeInvalidArgument)})
		return
	}
	h.sourceError(w, err)
}

// renderRun reads the run and its stored events and renders the detail page. A
// rejection is shown above the run but never closes the gate.
func (h *Handler) renderRun(w http.ResponseWriter, r *http.Request, status int, rejection *Rejection) {
	ctx := r.Context()
	p, ok := principalFrom(ctx)
	if !ok {
		h.denied(w, http.StatusUnauthorized, core.CodeUnauthenticated)
		return
	}
	run, err := h.run(ctx, p, r.PathValue("id"))
	if err != nil {
		h.sourceError(w, err)
		return
	}
	events, err := h.opts.Source.Events(ctx, p, run.ID, 0)
	if err != nil {
		h.sourceError(w, err)
		return
	}
	events = ordered(events)
	view := runView{
		Prefix:    h.prefix,
		Run:       run,
		Events:    events,
		Recording: recordingViewOf(run.Recording),
		Rejection: rejection,
	}
	body, err := json.Marshal(runJSON{Run: publicRun(run), Events: events})
	if err != nil {
		h.denied(w, http.StatusInternalServerError, core.CodeInternal)
		return
	}
	view.JSON = template.JS(body)
	h.render(w, status, "run.html", view)
}

// run reads one run and enforces tenant isolation a second time. A run of
// another tenant is not_found: the same answer as one that does not exist, so
// the inspector reveals nothing about it (REQ-INSP-002).
func (h *Handler) run(ctx context.Context, p authn.Principal, runID string) (Run, error) {
	if runID == "" {
		return Run{}, core.NewError(core.CodeInvalidArgument, "inspector: run id is empty", nil)
	}
	run, err := h.opts.Source.Run(ctx, p, runID)
	if err != nil {
		return Run{}, err
	}
	if run.Tenant != p.Tenant {
		return Run{}, core.NewError(core.CodeNotFound, "inspector: run not found", nil)
	}
	return run, nil
}

// sourceError maps a Source failure to its page. A denial never carries run
// data (REQ-INSP-013).
func (h *Handler) sourceError(w http.ResponseWriter, err error) {
	code := core.CodeOf(err)
	switch code {
	case core.CodeNotFound:
		h.denied(w, http.StatusNotFound, code)
	case core.CodePermissionDenied, core.CodeUnauthenticated:
		h.denied(w, http.StatusForbidden, code)
	case core.CodeInvalidArgument:
		h.denied(w, http.StatusBadRequest, code)
	default:
		h.denied(w, http.StatusInternalServerError, code)
	}
}

func (h *Handler) denied(w http.ResponseWriter, status int, code core.Code) {
	h.render(w, status, "denied.html", deniedView{Prefix: h.prefix, Code: string(code)})
}

func (h *Handler) render(w http.ResponseWriter, status int, name string, data any) {
	tmpl, ok := h.templates[name]
	if !ok {
		http.Error(w, "inspector: missing template "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := tmpl.Execute(w, data); err != nil {
		// The header is gone; the browser gets a truncated page. Nothing to do
		// but stop writing.
		return
	}
}

// normalisePrefix keeps an absolute mount prefix without a trailing slash.
func normalisePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return ""
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimRight(prefix, "/")
}

func int64Query(r *http.Request, name string) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, core.NewError(core.CodeInvalidArgument, "inspector: "+name+" must be a non-negative integer", nil)
	}
	return n, nil
}

// ordered sorts events by sequence and drops duplicates, so the page shows
// them in order even if a Source returns them out of order (REQ-INSP-004).
func ordered(events []Event) []Event {
	out := make([]Event, 0, len(events))
	seen := make(map[int64]bool, len(events))
	for _, e := range events {
		if seen[e.Seq] {
			continue
		}
		seen[e.Seq] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// writeJSON answers status with v encoded as JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers the stable platform code, never an English message that
// could echo a request value.
func writeError(w http.ResponseWriter, status int, code core.Code) {
	writeJSON(w, status, errorJSON{Error: errorBody{Code: code}})
}

// IsDenied reports whether err is the inspector's denial of another tenant's
// or another principal's record. Callers that wrap Source use it to keep a
// denial indistinguishable from a missing run.
func IsDenied(err error) bool {
	var ce *core.Error
	if !errors.As(err, &ce) {
		return false
	}
	return ce.Code == core.CodePermissionDenied || ce.Code == core.CodeNotFound
}
