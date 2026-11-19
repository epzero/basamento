package scale

import (
	"context"
	"fmt"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model/worker"
	"github.com/epzero/basamento/platform"
)

// LauncherError is an optional launcher capability: the last error it saw,
// which a degraded template reports to invocations at their queue timeout
// (REQ-MODEL-041). A launcher that does not implement it reports the cold
// start itself.
type LauncherError interface {
	LastLaunchError(ctx context.Context) string
}

// ScalerOptions tune the scaler. Zero values are safe: no deployment cap, no
// launch rate or cooldown, and a one-minute base back-off.
type ScalerOptions struct {
	Clock core.Clock
	Audit platform.Audit
	// Launchers maps a template's Launcher name to its implementation.
	Launchers map[string]platform.Launcher
	// DeploymentCap is the most workers on demand across every template; 0
	// means no cap.
	DeploymentCap int
	// LaunchRate is the most workers one template may be asked for per tick;
	// 0 means no limit.
	LaunchRate int
	// Cooldown is the least time between two launch requests for a template;
	// 0 means none.
	Cooldown time.Duration
	// Backoff is the base of the exponential wait after a failed launch;
	// 0 reads as one minute.
	Backoff time.Duration
}

// Scaler answers one question per template: how many more workers should be
// asked for now? It runs once per deployment; a lease in the KV store picks
// the replica (ADR-0012 §12). It never kills a worker and never reconciles a
// desired state beyond the template's minimum.
type Scaler struct {
	store     *worker.Store
	tokens    *TokenStore
	clock     core.Clock
	audit     platform.Audit
	launchers map[string]platform.Launcher
	cap       int
	rate      int
	cooldown  time.Duration
	backoff   time.Duration
}

// NewScaler builds the scaler over a worker store and a template store.
func NewScaler(store *worker.Store, tokens *TokenStore, opts ScalerOptions) (*Scaler, error) {
	if store == nil || tokens == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "scale: nil store", nil)
	}
	if opts.Clock == nil {
		opts.Clock = store.Clock()
	}
	if opts.Backoff <= 0 {
		opts.Backoff = time.Minute
	}
	return &Scaler{
		store: store, tokens: tokens, clock: opts.Clock, audit: opts.Audit,
		launchers: opts.Launchers, cap: opts.DeploymentCap, rate: opts.LaunchRate,
		cooldown: opts.Cooldown, backoff: opts.Backoff,
	}, nil
}

func (s *Scaler) now() time.Time { return s.clock.Now().UTC() }

// Tick expires launches past their cold-start deadlines and asks each
// template's launcher for the workers demand requires, within the template's
// minimum and maximum, the deployment cap, the launch rate and the cooldown.
// It is deterministic: one clock, one pass over templates ordered by id.
func (s *Scaler) Tick(ctx context.Context) error {
	templates, err := s.tokens.ListTemplates(ctx, "")
	if err != nil {
		return err
	}
	for _, tmpl := range templates {
		tmpl, err := s.expire(ctx, tmpl)
		if err != nil {
			return err
		}
		deploymentPresent, deploymentPending, err := s.deploymentCounts(ctx, templates)
		if err != nil {
			return err
		}
		now := s.now()
		if tmpl.State == StateDegraded && now.Before(tmpl.NextAttempt) {
			continue
		}
		if s.cooldown > 0 && !tmpl.LastLaunch.IsZero() && now.Before(tmpl.LastLaunch.Add(s.cooldown)) {
			continue
		}
		launch, err := s.want(ctx, tmpl, deploymentPresent, deploymentPending)
		if err != nil {
			return err
		}
		if launch <= 0 {
			continue
		}
		if s.rate > 0 && launch > s.rate {
			launch = s.rate
		}
		if err := s.launch(ctx, tmpl, launch); err != nil {
			return err
		}
	}
	return nil
}

// deploymentCounts counts present template workers and pending launches over
// every template. A manually joined worker is not on-demand capacity.
func (s *Scaler) deploymentCounts(ctx context.Context, templates []Template) (int, int, error) {
	pools, err := s.store.ListPools(ctx)
	if err != nil {
		return 0, 0, err
	}
	present := 0
	for _, pool := range pools {
		workers, err := s.store.ListWorkers(ctx, pool)
		if err != nil {
			return 0, 0, err
		}
		for _, w := range workers {
			if w.Present && w.Template != "" {
				present++
			}
		}
	}
	pending := 0
	for _, tmpl := range templates {
		launches, err := s.tokens.pending(ctx, tmpl.ID)
		if err != nil {
			return 0, 0, err
		}
		for _, p := range launches {
			pending += p.Count - p.Joined
		}
	}
	return present, pending, nil
}

// expire turns launches past their deadline into failures. Three in a row
// degrade the template; it then tries one worker per back-off period, and the
// pool's queue timeouts report the launcher's last error (REQ-MODEL-041).
func (s *Scaler) expire(ctx context.Context, tmpl Template) (Template, error) {
	launches, err := s.tokens.pending(ctx, tmpl.ID)
	if err != nil {
		return tmpl, err
	}
	changed := false
	now := s.now()
	for _, p := range launches {
		if p.Joined >= p.Count || now.Before(p.Deadline) {
			continue
		}
		if err := s.tokens.markLaunchFailed(ctx, p.ID); err != nil {
			return tmpl, err
		}
		msg := s.lastError(ctx, tmpl)
		tmpl.Failures++
		tmpl.LastError = msg
		if tmpl.Failures >= 3 {
			tmpl.State = StateDegraded
		}
		tmpl.NextAttempt = now.Add(s.backoffFor(tmpl.Failures))
		changed = true
		if err := s.store.SetLaunchError(ctx, tmpl.Pool, msg); err != nil {
			return tmpl, err
		}
		s.auditEvent(ctx, "launch.fail", tmpl.ID, "failed", "cold_start")
	}
	if changed {
		if err := s.tokens.saveRuntime(ctx, tmpl); err != nil {
			return tmpl, err
		}
	}
	return tmpl, nil
}

func (s *Scaler) lastError(ctx context.Context, tmpl Template) string {
	if l := s.launchers[tmpl.Launcher]; l != nil {
		if reporter, ok := l.(LauncherError); ok {
			if msg := reporter.LastLaunchError(ctx); msg != "" {
				return msg
			}
		}
	}
	return "the launch did not join by its cold-start deadline"
}

func (s *Scaler) backoffFor(failures int) time.Duration {
	d := s.backoff
	for i := 1; i < failures && d < 24*time.Hour; i++ {
		d *= 2
	}
	if d > 24*time.Hour {
		d = 24 * time.Hour
	}
	return d
}

// want is the number of workers to ask for now (ADR-0012 §12):
// demand = queued units the template serves, less free units of present
// workers and the capacity of pending launches; wanted = demand / capacity
// rounded up, together with present and pending, kept between min and max and
// under the deployment cap.
func (s *Scaler) want(ctx context.Context, tmpl Template, deploymentPresent, deploymentPending int) (int, error) {
	pool, err := s.store.Pool(ctx, tmpl.Pool)
	if err != nil {
		return 0, err
	}
	waiting, err := s.store.WaitingInvocations(ctx, tmpl.Pool)
	if err != nil {
		return 0, err
	}
	served := map[string]bool{}
	for _, d := range tmpl.Descriptors {
		served[d.ID] = true
	}
	limited := map[core.TenantID]int{}
	units := 0
	for _, inv := range waiting {
		if !served[inv.Model] {
			continue
		}
		if pool.Tenant == "" {
			settings, err := s.store.TenantSettings(ctx, tmpl.Pool, inv.Tenant)
			if err != nil {
				return 0, err
			}
			if settings.Concurrency > 0 && limited[inv.Tenant] >= settings.Concurrency {
				continue
			}
			limited[inv.Tenant]++
		}
		if inv.Units > 0 {
			units += inv.Units
		} else {
			units++
		}
	}
	workers, err := s.store.ListWorkers(ctx, tmpl.Pool)
	if err != nil {
		return 0, err
	}
	present, free := 0, 0
	for _, w := range workers {
		if !w.Present || w.State != worker.StateActive {
			continue
		}
		if w.Template == tmpl.ID {
			present++
		}
		if servesAny(w, served) {
			f := w.Capacity - w.UnitsInUse
			if f > 0 {
				free += f
			}
		}
	}
	launches, err := s.tokens.pending(ctx, tmpl.ID)
	if err != nil {
		return 0, err
	}
	pending := 0
	for _, p := range launches {
		if n := p.Count - p.Joined; n > 0 {
			pending += n
		}
	}
	demand := units - free - pending*tmpl.Capacity
	if demand < 0 {
		demand = 0
	}
	needed := ceilDiv(demand, tmpl.Capacity)
	total := present + pending
	if tmpl.State == StateDegraded {
		// One worker per back-off period, whatever the demand.
		if needed > 1 {
			needed = 1
		}
		if needed < 1 {
			needed = 1
		}
	}
	if total+needed < tmpl.Min {
		needed = tmpl.Min - total
	}
	if allowed := tmpl.Max - total; needed > allowed {
		needed = allowed
	}
	if s.cap > 0 {
		if allowed := s.cap - deploymentPresent - deploymentPending; needed > allowed {
			needed = allowed
		}
	}
	if needed < 0 {
		needed = 0
	}
	return needed, nil
}

func servesAny(w worker.Worker, served map[string]bool) bool {
	for _, d := range w.Descriptors {
		if served[d.ID] {
			return true
		}
	}
	return false
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func (s *Scaler) launch(ctx context.Context, tmpl Template, count int) error {
	now := s.now()
	deadline := now.Add(tmpl.ColdStart)
	launchID, tokens, err := s.tokens.MintLaunch(ctx, tmpl, count, deadline)
	if err != nil {
		return err
	}
	tmpl.LastLaunch = now
	if err := s.tokens.saveRuntime(ctx, tmpl); err != nil {
		return err
	}
	launcher := s.launchers[tmpl.Launcher]
	if launcher == nil {
		return s.fail(ctx, tmpl, launchID, core.NewError(core.CodeUnavailable,
			fmt.Sprintf("launcher %q is not configured", tmpl.Launcher), map[string]any{"launcher": tmpl.Launcher}))
	}
	settings := make(map[string]string, len(tmpl.Settings)+3)
	for k, v := range tmpl.Settings {
		settings[k] = v
	}
	// The worker's exit policy travels with the launch, so the worker leaves
	// by itself (ADR-0012 §12).
	if tmpl.IdleExit > 0 {
		settings["idle_exit"] = tmpl.IdleExit.String()
	}
	if tmpl.MaxLifetime > 0 {
		settings["max_lifetime"] = tmpl.MaxLifetime.String()
	}
	if tmpl.MaxInvocations > 0 {
		settings["max_invocations"] = fmt.Sprintf("%d", tmpl.MaxInvocations)
	}
	res, err := launcher.Launch(ctx, platform.LaunchRequest{
		Pool:           tmpl.Pool,
		Template:       tmpl.ID,
		Count:          count,
		JoinTokens:     tokens,
		IdempotencyKey: launchID,
		Credential:     tmpl.Credential,
		Settings:       settings,
	})
	if err != nil {
		return s.fail(ctx, tmpl, launchID, err)
	}
	if !res.Accepted {
		return s.fail(ctx, tmpl, launchID, core.NewError(core.CodeUnavailable,
			"the launcher refused the launch", map[string]any{"template": tmpl.ID}))
	}
	s.auditEvent(ctx, "launch.request", tmpl.ID, "allowed", "")
	return nil
}

// fail records a refused or failed launch. It counts like a launch that did
// not join, so repeated failures back off and eventually degrade.
func (s *Scaler) fail(ctx context.Context, tmpl Template, launchID string, cause error) error {
	if err := s.tokens.markLaunchFailed(ctx, launchID); err != nil {
		return err
	}
	msg := cause.Error()
	tmpl.Failures++
	tmpl.LastError = msg
	if tmpl.Failures >= 3 {
		tmpl.State = StateDegraded
	}
	tmpl.NextAttempt = s.now().Add(s.backoffFor(tmpl.Failures))
	if err := s.tokens.saveRuntime(ctx, tmpl); err != nil {
		return err
	}
	if err := s.store.SetLaunchError(ctx, tmpl.Pool, msg); err != nil {
		return err
	}
	s.auditEvent(ctx, "launch.fail", tmpl.ID, "failed", string(core.CodeOf(cause)))
	return nil
}

func (s *Scaler) auditEvent(ctx context.Context, action, resource, outcome, reason string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, platform.AuditEvent{
		Time: s.now(), Action: action, Resource: resource, Outcome: outcome, Reason: reason,
	})
}

// TemplateState reads a template's state after a tick.
func (s *Scaler) TemplateState(ctx context.Context, id string) (State, error) {
	tmpl, err := s.tokens.Template(ctx, id)
	if err != nil {
		return "", err
	}
	return tmpl.State, nil
}
