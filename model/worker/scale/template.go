package scale

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model/worker"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
)

// State is what a template is doing. A template becomes degraded after three
// launches in a row do not join by their cold-start deadlines (REQ-MODEL-041).
type State string

const (
	// StateActive keeps the template's minimum and launches for demand.
	StateActive State = "active"
	// StateDegraded tries one worker per back-off period and lets waiting
	// invocations fail at their queue timeout with the launcher's last error.
	StateDegraded State = "degraded"
)

// Template describes workers that do not exist yet: what one serves, how many
// to keep and to allow, how long a cold start takes, when a worker leaves, and
// which launcher starts it (ADR-0012 §12).
type Template struct {
	ID          string
	Pool        string
	Descriptors []worker.Descriptor
	Labels      map[string]string
	Capacity    int
	Min         int
	Max         int
	ColdStart   time.Duration
	IdleExit    time.Duration
	MaxLifetime time.Duration
	// MaxInvocations is passed to the worker; 0 means no limit.
	MaxInvocations int
	Launcher       string
	Settings       map[string]string
	Credential     string

	// State, Failures, LastError, NextAttempt and LastLaunch are runtime
	// state the scaler keeps.
	State       State
	Failures    int
	LastError   string
	NextAttempt time.Time
	LastLaunch  time.Time
}

// Validate checks a template. A pool's queue timeout must be at least twice
// the cold start when the pool sets one; the check runs again when the
// template is saved.
func (t Template) Validate() error {
	if strings.TrimSpace(t.ID) == "" {
		return core.NewError(core.CodeInvalidArgument, "worker template has no id", nil)
	}
	if strings.TrimSpace(t.Pool) == "" {
		return core.NewError(core.CodeInvalidArgument, "worker template has no pool", nil)
	}
	if len(t.Descriptors) == 0 {
		return core.NewError(core.CodeInvalidArgument, "worker template serves no model", nil)
	}
	seen := map[string]bool{}
	for _, d := range t.Descriptors {
		if strings.TrimSpace(d.ID) == "" {
			return core.NewError(core.CodeInvalidArgument, "worker template has a descriptor without a model id", nil)
		}
		if seen[d.ID] {
			return core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("worker template declares model %q twice", d.ID), map[string]any{"model": d.ID})
		}
		seen[d.ID] = true
	}
	if t.Capacity <= 0 {
		return core.NewError(core.CodeInvalidArgument, "worker template capacity must be positive", nil)
	}
	if t.Min < 0 || t.Max < 1 || t.Max < t.Min {
		return core.NewError(core.CodeInvalidArgument, "worker template needs 0 <= minimum <= maximum", nil)
	}
	if t.ColdStart < 0 {
		return core.NewError(core.CodeInvalidArgument, "worker template cold start must not be negative", nil)
	}
	if t.IdleExit < 0 || t.MaxLifetime < 0 {
		return core.NewError(core.CodeInvalidArgument, "worker template exit times must not be negative", nil)
	}
	if strings.TrimSpace(t.Launcher) == "" {
		return core.NewError(core.CodeInvalidArgument, "worker template has no launcher", nil)
	}
	return nil
}

// Options tune a TokenStore.
type Options struct {
	Clock core.Clock
	IDs   core.Ids
	Audit platform.Audit
	// NewToken mints a join token. Nil uses crypto/rand; the gate injects a
	// deterministic sequence so a scenario can name t1.
	NewToken func() string
}

// TokenStore owns worker templates, their launches and their single-use join
// tokens. It implements worker.TemplateCatalog and worker.LaunchTokens.
type TokenStore struct {
	db       *psql.Store
	clock    core.Clock
	ids      core.Ids
	audit    platform.Audit
	newToken func() string
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS worker_scale_templates (
		template_id TEXT PRIMARY KEY,
		pool_id TEXT NOT NULL,
		descriptors TEXT NOT NULL,
		labels TEXT NOT NULL,
		capacity INTEGER NOT NULL,
		min_workers INTEGER NOT NULL,
		max_workers INTEGER NOT NULL,
		cold_start_ms INTEGER NOT NULL,
		idle_exit_ms INTEGER NOT NULL,
		max_lifetime_ms INTEGER NOT NULL,
		max_invocations INTEGER NOT NULL DEFAULT 0,
		launcher TEXT NOT NULL,
		settings TEXT NOT NULL,
		credential TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'active',
		failures INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '',
		next_attempt_unix_ms INTEGER NOT NULL DEFAULT 0,
		last_launch_unix_ms INTEGER NOT NULL DEFAULT 0,
		created_unix_ms INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS worker_scale_launches (
		launch_id TEXT PRIMARY KEY,
		template_id TEXT NOT NULL,
		pool_id TEXT NOT NULL,
		count INTEGER NOT NULL,
		tokens TEXT NOT NULL,
		requested_unix_ms INTEGER NOT NULL,
		deadline_unix_ms INTEGER NOT NULL,
		joined INTEGER NOT NULL DEFAULT 0,
		failed INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS worker_scale_tokens (
		token_hash TEXT PRIMARY KEY,
		launch_id TEXT NOT NULL,
		template_id TEXT NOT NULL,
		pool_id TEXT NOT NULL,
		descriptors TEXT NOT NULL,
		labels TEXT NOT NULL,
		capacity INTEGER NOT NULL,
		expires_unix_ms INTEGER NOT NULL,
		used INTEGER NOT NULL DEFAULT 0
	)`,
}

// OpenTokens opens the template store and creates its schema.
func OpenTokens(ctx context.Context, db *psql.Store, opts Options) (*TokenStore, error) {
	if db == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "scale: nil sql store", nil)
	}
	if opts.Clock == nil {
		opts.Clock = core.SystemClock{}
	}
	if opts.IDs == nil {
		opts.IDs = core.NewULIDs(opts.Clock, nil)
	}
	if opts.NewToken == nil {
		opts.NewToken = randomToken
	}
	t := &TokenStore{db: db, clock: opts.Clock, ids: opts.IDs, audit: opts.Audit, newToken: opts.NewToken}
	for _, q := range schema {
		if _, err := db.Execer().Exec(ctx, q); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (t *TokenStore) now() time.Time { return t.clock.Now().UTC() }

func randomToken() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func unixms(t time.Time) int64 { return t.UTC().UnixNano() / int64(time.Millisecond) }

func fromms(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// SaveTemplate stores or updates a template's configuration, keeping its
// runtime state. It fails with conflict when the pool is missing or its queue
// timeout is shorter than twice the cold start.
func (t *TokenStore) SaveTemplate(ctx context.Context, tmpl Template) error {
	if err := tmpl.Validate(); err != nil {
		return err
	}
	var (
		queueMS int64
		poolID  string
	)
	err := t.db.Execer().QueryRow(ctx,
		`SELECT pool_id, queue_timeout_ms FROM worker_pools WHERE pool_id = ?`, tmpl.Pool).Scan(&poolID, &queueMS)
	if core.CodeOf(err) == core.CodeNotFound {
		return core.NewError(core.CodeNotFound, fmt.Sprintf("pool %q does not exist", tmpl.Pool), map[string]any{"pool": tmpl.Pool})
	}
	if err != nil {
		return err
	}
	queueTimeout := time.Duration(queueMS) * time.Millisecond
	if queueTimeout > 0 && queueTimeout < 2*tmpl.ColdStart {
		return core.NewError(core.CodeInvalidArgument,
			"the pool's queue timeout must be at least twice the template's cold start",
			map[string]any{"pool": tmpl.Pool, "queue_timeout_ms": queueTimeout.Milliseconds(), "cold_start_ms": tmpl.ColdStart.Milliseconds()})
	}
	desc, err := json.Marshal(tmpl.Descriptors)
	if err != nil {
		return err
	}
	labels, err := json.Marshal(tmpl.Labels)
	if err != nil {
		return err
	}
	settings, err := json.Marshal(tmpl.Settings)
	if err != nil {
		return err
	}
	_, err = t.db.Execer().Exec(ctx,
		`INSERT INTO worker_scale_templates
		 (template_id, pool_id, descriptors, labels, capacity, min_workers, max_workers, cold_start_ms,
		  idle_exit_ms, max_lifetime_ms, max_invocations, launcher, settings, credential, state, failures,
		  last_error, next_attempt_unix_ms, last_launch_unix_ms, created_unix_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 0, '', 0, 0, ?)
		 ON CONFLICT (template_id) DO UPDATE SET
		   pool_id = excluded.pool_id, descriptors = excluded.descriptors, labels = excluded.labels,
		   capacity = excluded.capacity, min_workers = excluded.min_workers, max_workers = excluded.max_workers,
		   cold_start_ms = excluded.cold_start_ms, idle_exit_ms = excluded.idle_exit_ms,
		   max_lifetime_ms = excluded.max_lifetime_ms, max_invocations = excluded.max_invocations,
		   launcher = excluded.launcher, settings = excluded.settings, credential = excluded.credential`,
		tmpl.ID, tmpl.Pool, string(desc), string(labels), tmpl.Capacity, tmpl.Min, tmpl.Max,
		tmpl.ColdStart.Milliseconds(), tmpl.IdleExit.Milliseconds(), tmpl.MaxLifetime.Milliseconds(),
		tmpl.MaxInvocations, tmpl.Launcher, string(settings), tmpl.Credential, unixms(t.now()))
	return err
}

// Template reads one template.
func (t *TokenStore) Template(ctx context.Context, id string) (Template, error) {
	row := t.db.Execer().QueryRow(ctx, selectTemplate+` WHERE template_id = ?`, id)
	tmpl, err := scanTemplate(row)
	if core.CodeOf(err) == core.CodeNotFound {
		return Template{}, core.NewError(core.CodeNotFound, fmt.Sprintf("worker template %q does not exist", id), map[string]any{"template": id})
	}
	return tmpl, err
}

// ListTemplates lists templates, by id, optionally only those of one pool.
func (t *TokenStore) ListTemplates(ctx context.Context, poolID string) ([]Template, error) {
	query := selectTemplate
	var args []any
	if poolID != "" {
		query += ` WHERE pool_id = ?`
		args = append(args, poolID)
	}
	query += ` ORDER BY template_id`
	rows, err := t.db.Execer().Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Template
	for rows.Next() {
		tmpl, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tmpl)
	}
	return out, rows.Err()
}

const selectTemplate = `SELECT template_id, pool_id, descriptors, labels, capacity, min_workers,
	max_workers, cold_start_ms, idle_exit_ms, max_lifetime_ms, max_invocations, launcher, settings,
	credential, state, failures, last_error, next_attempt_unix_ms, last_launch_unix_ms
	FROM worker_scale_templates`

type scanner interface {
	Scan(dest ...any) error
}

func scanTemplate(row scanner) (Template, error) {
	var (
		tmpl                         Template
		desc, labels, settings       string
		cold, idle, life, next, last int64
		state                        string
	)
	err := row.Scan(&tmpl.ID, &tmpl.Pool, &desc, &labels, &tmpl.Capacity, &tmpl.Min, &tmpl.Max,
		&cold, &idle, &life, &tmpl.MaxInvocations, &tmpl.Launcher, &settings, &tmpl.Credential,
		&state, &tmpl.Failures, &tmpl.LastError, &next, &last)
	if err != nil {
		return Template{}, err
	}
	if err := json.Unmarshal([]byte(desc), &tmpl.Descriptors); err != nil {
		return Template{}, err
	}
	if err := json.Unmarshal([]byte(labels), &tmpl.Labels); err != nil {
		return Template{}, err
	}
	if err := json.Unmarshal([]byte(settings), &tmpl.Settings); err != nil {
		return Template{}, err
	}
	if tmpl.Labels == nil {
		tmpl.Labels = map[string]string{}
	}
	if tmpl.Settings == nil {
		tmpl.Settings = map[string]string{}
	}
	tmpl.ColdStart = time.Duration(cold) * time.Millisecond
	tmpl.IdleExit = time.Duration(idle) * time.Millisecond
	tmpl.MaxLifetime = time.Duration(life) * time.Millisecond
	tmpl.NextAttempt = fromms(next)
	tmpl.LastLaunch = fromms(last)
	tmpl.State = State(state)
	return tmpl, nil
}

// TemplateModels implements worker.TemplateCatalog: what a pool's templates
// would serve, with the shortest cold start when several serve a model.
func (t *TokenStore) TemplateModels(ctx context.Context, poolID string) ([]worker.TemplateModel, error) {
	templates, err := t.ListTemplates(ctx, poolID)
	if err != nil {
		return nil, err
	}
	byID := map[string]worker.TemplateModel{}
	for _, tmpl := range templates {
		for _, d := range tmpl.Descriptors {
			got, ok := byID[d.ID]
			if !ok || tmpl.ColdStart < got.ColdStart {
				byID[d.ID] = worker.TemplateModel{Descriptor: d, ColdStart: tmpl.ColdStart}
			}
		}
	}
	out := make([]worker.TemplateModel, 0, len(byID))
	for id := range byID {
		out = append(out, byID[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Descriptor.ID < out[j].Descriptor.ID })
	return out, nil
}

// MintLaunch records a pending launch and returns its id and one single-use
// join token per worker. The tokens never leave the platform except through a
// launcher.
func (t *TokenStore) MintLaunch(ctx context.Context, tmpl Template, count int, deadline time.Time) (string, []string, error) {
	if count <= 0 {
		return "", nil, core.NewError(core.CodeInvalidArgument, "launch needs a positive count", nil)
	}
	launchID, err := t.ids.New("launch")
	if err != nil {
		return "", nil, err
	}
	tokens := make([]string, count)
	desc, err := json.Marshal(tmpl.Descriptors)
	if err != nil {
		return "", nil, err
	}
	labels, err := json.Marshal(tmpl.Labels)
	if err != nil {
		return "", nil, err
	}
	err = t.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		for i := range tokens {
			token := t.newToken()
			if token == "" {
				return core.NewError(core.CodeInternal, "scale: no randomness", nil)
			}
			tokens[i] = token
			if _, err := tx.Exec(ctx,
				`INSERT INTO worker_scale_tokens
				 (token_hash, launch_id, template_id, pool_id, descriptors, labels, capacity, expires_unix_ms, used)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
				hashToken(token), launchID, tmpl.ID, tmpl.Pool, string(desc), string(labels),
				tmpl.Capacity, unixms(deadline)); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(tokens)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO worker_scale_launches
			 (launch_id, template_id, pool_id, count, tokens, requested_unix_ms, deadline_unix_ms, joined, failed)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0)`,
			launchID, tmpl.ID, tmpl.Pool, count, string(raw), unixms(t.now()), unixms(deadline))
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return launchID, tokens, nil
}

// ConsumeLaunchToken implements worker.LaunchTokens. A token is spent once,
// expires at its launch's cold-start deadline, and grants exactly its
// template's descriptors, labels and capacity.
func (t *TokenStore) ConsumeLaunchToken(ctx context.Context, token string) (worker.LaunchGrant, error) {
	if strings.TrimSpace(token) == "" {
		return worker.LaunchGrant{}, unauthenticated()
	}
	var (
		grant                  worker.LaunchGrant
		launchID, desc, labels string
		expires, used          int64
		capacity               int
	)
	err := t.db.Execer().QueryRow(ctx,
		`SELECT launch_id, template_id, pool_id, descriptors, labels, capacity, expires_unix_ms, used
		 FROM worker_scale_tokens WHERE token_hash = ?`, hashToken(token)).
		Scan(&launchID, &grant.Template, &grant.Pool, &desc, &labels, &capacity, &expires, &used)
	if core.CodeOf(err) == core.CodeNotFound {
		return worker.LaunchGrant{}, unauthenticated()
	}
	if err != nil {
		return worker.LaunchGrant{}, err
	}
	if used != 0 || t.now().UnixMilli() >= expires {
		return worker.LaunchGrant{}, unauthenticated()
	}
	res, err := t.db.Execer().Exec(ctx,
		`UPDATE worker_scale_tokens SET used = 1 WHERE token_hash = ? AND used = 0 AND expires_unix_ms > ?`,
		hashToken(token), t.now().UnixMilli())
	if err != nil {
		return worker.LaunchGrant{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return worker.LaunchGrant{}, unauthenticated()
	}
	if err := json.Unmarshal([]byte(desc), &grant.Descriptors); err != nil {
		return worker.LaunchGrant{}, err
	}
	if err := json.Unmarshal([]byte(labels), &grant.Labels); err != nil {
		return worker.LaunchGrant{}, err
	}
	grant.Capacity = capacity
	// A worker joined: the launch succeeded and the template is healthy.
	if _, err := t.db.Execer().Exec(ctx,
		`UPDATE worker_scale_launches SET joined = joined + 1 WHERE launch_id = ?`, launchID); err != nil {
		return worker.LaunchGrant{}, err
	}
	if _, err := t.db.Execer().Exec(ctx,
		`UPDATE worker_scale_templates SET state = 'active', failures = 0, last_error = '',
		 next_attempt_unix_ms = 0 WHERE template_id = ?`, grant.Template); err != nil {
		return worker.LaunchGrant{}, err
	}
	return grant, nil
}

// TemplateForToken names the template a join token belongs to without
// spending it.
func (t *TokenStore) TemplateForToken(ctx context.Context, token string) (Template, error) {
	var id string
	err := t.db.Execer().QueryRow(ctx,
		`SELECT template_id FROM worker_scale_tokens WHERE token_hash = ?`, hashToken(token)).Scan(&id)
	if core.CodeOf(err) == core.CodeNotFound {
		return Template{}, unauthenticated()
	}
	if err != nil {
		return Template{}, err
	}
	return t.Template(ctx, id)
}

func unauthenticated() error {
	return core.NewError(core.CodeUnauthenticated, "join token is unknown, spent or expired", nil)
}

// pendingLaunch is one launch whose workers have not all joined.
type pendingLaunch struct {
	ID       string
	Template string
	Pool     string
	Count    int
	Deadline time.Time
	Joined   int
}

func (t *TokenStore) pending(ctx context.Context, templateID string) ([]pendingLaunch, error) {
	rows, err := t.db.Execer().Query(ctx,
		`SELECT launch_id, template_id, pool_id, count, deadline_unix_ms, joined, failed
		 FROM worker_scale_launches WHERE template_id = ? AND failed = 0 ORDER BY requested_unix_ms, launch_id`,
		templateID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []pendingLaunch
	for rows.Next() {
		var (
			p        pendingLaunch
			deadline int64
			failed   int
		)
		if err := rows.Scan(&p.ID, &p.Template, &p.Pool, &p.Count, &deadline, &p.Joined, &failed); err != nil {
			return nil, err
		}
		p.Deadline = fromms(deadline)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (t *TokenStore) markLaunchFailed(ctx context.Context, launchID string) error {
	_, err := t.db.Execer().Exec(ctx,
		`UPDATE worker_scale_launches SET failed = 1 WHERE launch_id = ?`, launchID)
	return err
}

func (t *TokenStore) saveRuntime(ctx context.Context, tmpl Template) error {
	_, err := t.db.Execer().Exec(ctx,
		`UPDATE worker_scale_templates SET state = ?, failures = ?, last_error = ?,
		 next_attempt_unix_ms = ?, last_launch_unix_ms = ? WHERE template_id = ?`,
		string(tmpl.State), tmpl.Failures, tmpl.LastError, unixms(tmpl.NextAttempt),
		unixms(tmpl.LastLaunch), tmpl.ID)
	return err
}
