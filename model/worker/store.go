package worker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
)

// ProtocolVersion is the worker contract version this implementation speaks.
// A worker whose version differs is refused with a message that says which
// side to upgrade (ADR-0012 §4).
const ProtocolVersion = "basamento.worker.v1"

// Options tune a Store. Clock and IDs are required in the gate: the fake
// clock makes ordering and leases deterministic.
type Options struct {
	Clock core.Clock
	Audit platform.Audit
	IDs   core.Ids
	// HeartbeatInterval is the cadence the join answer tells a worker to
	// use; zero reads as DefaultHeartbeatInterval.
	HeartbeatInterval time.Duration
	// MaxCostMpts bounds the cost a worker may report for one invocation
	// (ADR-0012 §9). Zero reads as DefaultMaxCostMpts; a reservation lower
	// than this is the tighter bound.
	MaxCostMpts int64
	// Recover extends the leases of an outage when the platform starts. A
	// worker process must leave it false: only a platform start recovers
	// (REQ-MODEL-027).
	Recover bool
	// Retention is how long an ended invocation and its events are kept;
	// zero reads as model.DefaultRetention.
	Retention time.Duration
	// MaxEnded is how many ended invocations are kept; zero reads as
	// model.DefaultMaxEnded.
	MaxEnded int
}

// Store is the platform side of the worker layer: pools, workers and the SQL
// queue. It is safe for concurrent use; one store may serve several platform
// replicas over the same database.
type Store struct {
	db        *psql.Store
	clock     core.Clock
	audit     platform.Audit
	ids       core.Ids
	heartbeat time.Duration
	maxCost   int64
	retention time.Duration
	maxEnded  int
}

// DefaultMaxCostMpts is the ceiling for a worker-reported cost when the
// invocation carries no reservation.
const DefaultMaxCostMpts int64 = 1_000_000

// schema is created on first use. The queue is a table and the bus only wakes
// lease requests, so both profiles behave the same and several replicas can
// serve one pool (ADR-0012 §3).
var schema = []string{
	`CREATE TABLE IF NOT EXISTS worker_pools (
		pool_id TEXT PRIMARY KEY,
		tenant TEXT NOT NULL,
		token_hash TEXT NOT NULL,
		token_revoked INTEGER NOT NULL DEFAULT 0,
		ageing_ms INTEGER NOT NULL,
		lease_ms INTEGER NOT NULL,
		grace_ms INTEGER NOT NULL,
		created_unix_ms INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS worker_pool_models (
		pool_id TEXT NOT NULL,
		model TEXT NOT NULL,
		descriptor TEXT NOT NULL,
		PRIMARY KEY (pool_id, model)
	)`,
	`CREATE TABLE IF NOT EXISTS worker_tenants (
		pool_id TEXT NOT NULL,
		tenant TEXT NOT NULL,
		weight INTEGER NOT NULL DEFAULT 1,
		concurrency INTEGER NOT NULL DEFAULT 0,
		spill_over INTEGER NOT NULL DEFAULT 0,
		spill_after_ms INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (pool_id, tenant)
	)`,
	`CREATE TABLE IF NOT EXISTS worker_workers (
		worker_id TEXT PRIMARY KEY,
		pool_id TEXT NOT NULL,
		state TEXT NOT NULL,
		descriptors TEXT NOT NULL,
		labels TEXT NOT NULL,
		capacity INTEGER NOT NULL,
		units_used INTEGER NOT NULL DEFAULT 0,
		present INTEGER NOT NULL DEFAULT 1,
		removed INTEGER NOT NULL DEFAULT 0,
		credential_hash TEXT NOT NULL,
		previous_credential_hash TEXT NOT NULL DEFAULT '',
		credential_expires_unix_ms INTEGER NOT NULL DEFAULT 0,
		previous_credential_expires_unix_ms INTEGER NOT NULL DEFAULT 0,
		joined_unix_ms INTEGER NOT NULL,
		heartbeat_ms INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS worker_invocations (
		client_ref TEXT PRIMARY KEY,
		pool_id TEXT NOT NULL,
		tenant TEXT NOT NULL,
		model TEXT NOT NULL,
		class TEXT NOT NULL,
		units INTEGER NOT NULL,
		state TEXT NOT NULL,
		worker_id TEXT NOT NULL DEFAULT '',
		attempt INTEGER NOT NULL DEFAULT 0,
		started INTEGER NOT NULL DEFAULT 0,
		in_doubt INTEGER NOT NULL DEFAULT 0,
		queued_unix_ms INTEGER NOT NULL,
		leased_unix_ms INTEGER NOT NULL DEFAULT 0,
		not_before_unix_ms INTEGER NOT NULL DEFAULT 0,
		lease_expires_unix_ms INTEGER NOT NULL DEFAULT 0,
		in_doubt_until_unix_ms INTEGER NOT NULL DEFAULT 0,
		queue_timeout_ms INTEGER NOT NULL,
		max_runtime_ms INTEGER NOT NULL DEFAULT 0,
		cancel_requested INTEGER NOT NULL DEFAULT 0,
		reserved_mpts INTEGER NOT NULL DEFAULT 0,
		charged_mpts INTEGER NOT NULL DEFAULT 0,
		code TEXT NOT NULL DEFAULT '',
		request TEXT NOT NULL DEFAULT '',
		result TEXT NOT NULL DEFAULT '',
		provider_handle TEXT NOT NULL DEFAULT '',
		spilled INTEGER NOT NULL DEFAULT 0,
		trace_parent TEXT NOT NULL DEFAULT '',
		ended_unix_ms INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS worker_events (
		client_ref TEXT NOT NULL,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL,
		data TEXT NOT NULL,
		PRIMARY KEY (client_ref, seq)
	)`,
	// A newer attempt tells the superseded worker to cancel at its provider
	// (ADR-0012 §7). The row is delivered in that worker's next heartbeat and
	// deleted then.
	`CREATE TABLE IF NOT EXISTS worker_cancels (
		client_ref TEXT NOT NULL,
		worker_id TEXT NOT NULL,
		attempt INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (client_ref, worker_id)
	)`,
}

// Open returns a store over db, creating the worker schema.
func Open(ctx context.Context, db *psql.Store, opts Options) (*Store, error) {
	if db == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "worker: nil sql store", nil)
	}
	if opts.Clock == nil {
		opts.Clock = core.SystemClock{}
	}
	if opts.IDs == nil {
		opts.IDs = core.NewULIDs(opts.Clock, nil)
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if opts.MaxCostMpts <= 0 {
		opts.MaxCostMpts = DefaultMaxCostMpts
	}
	if opts.Retention <= 0 {
		opts.Retention = model.DefaultRetention
	}
	if opts.MaxEnded <= 0 {
		opts.MaxEnded = model.DefaultMaxEnded
	}
	s := &Store{db: db, clock: opts.Clock, audit: opts.Audit, ids: opts.IDs, heartbeat: opts.HeartbeatInterval,
		maxCost: opts.MaxCostMpts, retention: opts.Retention, maxEnded: opts.MaxEnded}
	for _, q := range schema {
		if _, err := db.Execer().Exec(ctx, q); err != nil {
			return nil, err
		}
	}
	// Only a platform start recovers: every worker start opening the store
	// must not extend another worker's leases (REQ-MODEL-027).
	if opts.Recover {
		if err := s.Recover(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Clock returns the store's clock.
func (s *Store) Clock() core.Clock { return s.clock }

func (s *Store) now() time.Time { return s.clock.Now().UTC() }

func unixms(t time.Time) int64 { return t.UTC().UnixNano() / int64(time.Millisecond) }

func fromms(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// CreatePool stores a pool and returns its token once. Only the hash is kept.
func (s *Store) CreatePool(ctx context.Context, spec PoolSpec) (Pool, error) {
	if err := spec.validate(); err != nil {
		return Pool{}, err
	}
	token, err := newToken()
	if err != nil {
		return Pool{}, err
	}
	_, err = s.db.Execer().Exec(ctx,
		`INSERT INTO worker_pools (pool_id, tenant, token_hash, token_revoked, ageing_ms, lease_ms, grace_ms, created_unix_ms)
		 VALUES (?, ?, ?, 0, ?, ?, ?, ?)`,
		spec.ID, string(spec.Tenant), hashToken(token), spec.Ageing.Milliseconds(),
		spec.Lease.Milliseconds(), spec.Grace.Milliseconds(), unixms(s.now()))
	if err != nil {
		return Pool{}, err
	}
	return Pool{PoolSpec: spec, Token: token}, nil
}

// EnsurePool creates the pool when it does not exist. It is the composition
// convenience for a configuration file that names pools; it returns the token
// only for a new pool.
func (s *Store) EnsurePool(ctx context.Context, spec PoolSpec) (Pool, bool, error) {
	p, err := s.Pool(ctx, spec.ID)
	if err == nil {
		return Pool{PoolSpec: p}, false, nil
	}
	if core.CodeOf(err) != core.CodeNotFound {
		return Pool{}, false, err
	}
	created, err := s.CreatePool(ctx, spec)
	return created, err == nil, err
}

// Pool reads one pool by id, without its token.
func (s *Store) Pool(ctx context.Context, id string) (PoolSpec, error) {
	var p PoolSpec
	var tenant string
	var ageing, lease, grace int64
	err := s.db.Execer().QueryRow(ctx,
		`SELECT pool_id, tenant, ageing_ms, lease_ms, grace_ms FROM worker_pools WHERE pool_id = ?`, id).
		Scan(&p.ID, &tenant, &ageing, &lease, &grace)
	if core.CodeOf(err) == core.CodeNotFound {
		return PoolSpec{}, core.NewError(core.CodeNotFound, fmt.Sprintf("pool %q does not exist", id), map[string]any{"pool": id})
	}
	if err != nil {
		return PoolSpec{}, err
	}
	p.Tenant = core.TenantID(tenant)
	p.Ageing = time.Duration(ageing) * time.Millisecond
	p.Lease = time.Duration(lease) * time.Millisecond
	p.Grace = time.Duration(grace) * time.Millisecond
	return p, nil
}

// ListPools lists pool ids.
func (s *Store) ListPools(ctx context.Context) ([]string, error) {
	rows, err := s.db.Execer().Query(ctx, `SELECT pool_id FROM worker_pools ORDER BY pool_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RevokeToken revokes a pool's token: no further join succeeds and the
// refusal is audited (REQ-MODEL-029).
func (s *Store) RevokeToken(ctx context.Context, poolID string) error {
	res, err := s.db.Execer().Exec(ctx, `UPDATE worker_pools SET token_revoked = 1 WHERE pool_id = ?`, poolID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return core.NewError(core.CodeNotFound, fmt.Sprintf("pool %q does not exist", poolID), map[string]any{"pool": poolID})
	}
	return nil
}

func (s *Store) poolByToken(ctx context.Context, token string) (PoolSpec, error) {
	var p PoolSpec
	var tenant string
	var ageing, lease, grace int64
	err := s.db.Execer().QueryRow(ctx,
		`SELECT pool_id, tenant, ageing_ms, lease_ms, grace_ms FROM worker_pools
		 WHERE token_hash = ? AND token_revoked = 0`, hashToken(token)).
		Scan(&p.ID, &tenant, &ageing, &lease, &grace)
	if core.CodeOf(err) == core.CodeNotFound {
		return PoolSpec{}, core.NewError(core.CodeUnauthenticated, "pool token is unknown or revoked", nil)
	}
	if err != nil {
		return PoolSpec{}, err
	}
	p.Tenant = core.TenantID(tenant)
	p.Ageing = time.Duration(ageing) * time.Millisecond
	p.Lease = time.Duration(lease) * time.Millisecond
	p.Grace = time.Duration(grace) * time.Millisecond
	return p, nil
}

// Join exchanges a pool token for a worker id and a short-lived credential.
// A descriptor that differs from the one a present worker of the pool serves
// is refused with conflict (REQ-MODEL-028); an unknown or revoked token is
// refused with unauthenticated and audited (REQ-MODEL-029).
func (s *Store) Join(ctx context.Context, req JoinRequest) (Worker, error) {
	pool, err := s.poolByToken(ctx, req.PoolToken)
	if err != nil {
		s.auditEvent(ctx, "worker.refuse", s.poolIDByToken(ctx, req.PoolToken), "failed", "unauthenticated")
		return Worker{}, err
	}
	if req.ProtocolVersion != "" && req.ProtocolVersion != ProtocolVersion {
		s.auditEvent(ctx, "worker.refuse", pool.ID, "failed", "protocol")
		return Worker{}, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("worker protocol %q is incompatible; upgrade the side behind %s", req.ProtocolVersion, ProtocolVersion),
			map[string]any{"protocol_version": req.ProtocolVersion, "want": ProtocolVersion})
	}
	if req.Capacity <= 0 {
		return Worker{}, core.NewError(core.CodeInvalidArgument, "worker: capacity must be positive", nil)
	}
	if len(req.Descriptors) == 0 {
		return Worker{}, core.NewError(core.CodeInvalidArgument, "worker: join declares no descriptors", nil)
	}
	for _, d := range req.Descriptors {
		if strings.TrimSpace(d.ID) == "" {
			return Worker{}, core.NewError(core.CodeInvalidArgument, "worker: descriptor has no model id", nil)
		}
	}
	id, err := s.ids.New("worker")
	if err != nil {
		return Worker{}, err
	}
	credential, err := newToken()
	if err != nil {
		return Worker{}, err
	}
	descJSON, _ := json.Marshal(req.Descriptors)
	labelsJSON, _ := json.Marshal(normalizeLabels(req.Labels))
	err = s.db.InTx(ctx, func(ctx context.Context, tx *psql.Execer) error {
		// Lock the pool row so concurrent joins check and write one at a
		// time; a refused join then rolls back its worker row too
		// (REQ-MODEL-028).
		if _, err := tx.Exec(ctx,
			`UPDATE worker_pools SET created_unix_ms = created_unix_ms WHERE pool_id = ?`, pool.ID); err != nil {
			return err
		}
		if err := s.checkConflictTx(ctx, tx, pool.ID, req.Descriptors, s.now()); err != nil {
			return err
		}
		now := s.now()
		if _, err := tx.Exec(ctx,
			`INSERT INTO worker_workers (worker_id, pool_id, state, descriptors, labels, capacity, units_used, present, removed, credential_hash,
			   credential_expires_unix_ms, joined_unix_ms, heartbeat_ms)
			 VALUES (?, ?, ?, ?, ?, ?, 0, 1, 0, ?, ?, ?, ?)`,
			id, pool.ID, string(StateActive), string(descJSON), string(labelsJSON), req.Capacity,
			hashToken(credential), unixms(now.Add(s.credentialTTL())), unixms(now), unixms(now)); err != nil {
			return err
		}
		for _, d := range req.Descriptors {
			c := d.canonical()
			var existing string
			err := tx.QueryRow(ctx,
				`SELECT descriptor FROM worker_pool_models WHERE pool_id = ? AND model = ?`, pool.ID, d.ID).Scan(&existing)
			switch {
			case core.CodeOf(err) == core.CodeNotFound:
				if _, err := tx.Exec(ctx,
					`INSERT INTO worker_pool_models (pool_id, model, descriptor) VALUES (?, ?, ?)`,
					pool.ID, d.ID, c); err != nil {
					return err
				}
			case err != nil:
				return err
			case existing != c:
				// A descriptor in the pool with no present worker behind it (a
				// rollout that drained the old workers) is replaced by the new
				// declaration.
				if _, err := tx.Exec(ctx,
					`UPDATE worker_pool_models SET descriptor = ? WHERE pool_id = ? AND model = ?`,
					c, pool.ID, d.ID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		if core.CodeOf(err) == core.CodeConflict {
			s.auditEvent(ctx, "worker.refuse", pool.ID, "denied", "conflict")
		}
		return Worker{}, err
	}
	s.auditEvent(ctx, "worker.join", pool.ID, "allowed", "")
	return Worker{
		ID:                id,
		Pool:              pool.ID,
		State:             StateActive,
		Descriptors:       req.Descriptors,
		Labels:            normalizeLabels(req.Labels),
		Capacity:          req.Capacity,
		Present:           true,
		Credential:        credential,
		HeartbeatInterval: s.heartbeat,
	}, nil
}

// poolIDByToken names the pool a token belongs to, revoked or not, without
// ever returning the token itself.
func (s *Store) poolIDByToken(ctx context.Context, token string) string {
	var id string
	err := s.db.Execer().QueryRow(ctx,
		`SELECT pool_id FROM worker_pools WHERE token_hash = ?`, hashToken(token)).Scan(&id)
	if err != nil {
		return "[unknown]"
	}
	return id
}

func normalizeLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

func (s *Store) checkConflictTx(ctx context.Context, exec *psql.Execer, poolID string, descriptors []Descriptor, now time.Time) error {
	rows, err := exec.Query(ctx,
		`SELECT worker_id, descriptors, heartbeat_ms FROM worker_workers WHERE pool_id = ? AND present = 1 AND removed = 0`, poolID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	want := map[string]string{}
	for _, d := range descriptors {
		want[d.ID] = d.canonical()
	}
	for rows.Next() {
		var id, raw string
		var lastSeen int64
		if err := rows.Scan(&id, &raw, &lastSeen); err != nil {
			return err
		}
		if presenceStale(fromms(lastSeen), now, s.heartbeat) {
			// A crashed worker that missed its heartbeats does not hold a
			// descriptor against a replacement (ADR-0012 §2, §4).
			continue
		}
		var got []Descriptor
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			return core.NewError(core.CodeInternal, "worker: stored descriptors are corrupt",
				map[string]any{"worker": id, "cause": err.Error()})
		}
		for _, d := range got {
			if c, ok := want[d.ID]; ok && c != d.canonical() {
				return core.NewError(core.CodeConflict,
					fmt.Sprintf("model %q is already served with another descriptor", d.ID),
					map[string]any{"model": d.ID, "worker": id})
			}
		}
	}
	return rows.Err()
}

// Leave is a clean exit: the worker takes no more work and its descriptors
// stay in the pool, offline (ADR-0012 §2).
func (s *Store) Leave(ctx context.Context, workerID, credential string) error {
	w, err := s.authenticate(ctx, workerID, credential)
	if err != nil {
		return err
	}
	if _, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_workers SET present = 0, state = ?, credential_hash = '', previous_credential_hash = '',
			credential_expires_unix_ms = 0, previous_credential_expires_unix_ms = 0
		 WHERE worker_id = ?`, string(StateLeft), w.ID); err != nil {
		return err
	}
	if _, err := s.db.Execer().Exec(ctx, `DELETE FROM worker_cancels WHERE worker_id = ?`, w.ID); err != nil {
		return err
	}
	tenant := s.poolTenant(ctx, w.Pool)
	if err := s.auditEventFor(ctx, tenant, "worker.leave", w.Pool, "allowed", ""); err != nil {
		return auditUnavailable(err)
	}
	return nil
}

// Remove makes a worker's further calls fail with unauthenticated and takes it
// out of the queue (REQ-MODEL-030).
func (s *Store) Remove(ctx context.Context, workerID string) error {
	var pool string
	err := s.db.Execer().QueryRow(ctx,
		`SELECT pool_id FROM worker_workers WHERE worker_id = ?`, workerID).Scan(&pool)
	if core.CodeOf(err) == core.CodeNotFound {
		return core.NewError(core.CodeNotFound, fmt.Sprintf("worker %q does not exist", workerID), map[string]any{"worker": workerID})
	}
	if err != nil {
		return err
	}
	if _, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_workers SET removed = 1, present = 0, state = ? WHERE worker_id = ?`, string(StateLeft), workerID); err != nil {
		return err
	}
	if _, err := s.db.Execer().Exec(ctx, `DELETE FROM worker_cancels WHERE worker_id = ?`, workerID); err != nil {
		return err
	}
	s.auditEvent(ctx, "worker.remove", pool, "allowed", "")
	return nil
}

// SetState pauses or drains a worker. A drain takes no new work; the worker
// leaves by itself when it holds none (ADR-0012 §6).
func (s *Store) SetState(ctx context.Context, workerID string, state State) error {
	if state != StateActive && state != StateDraining && state != StatePaused {
		return core.NewError(core.CodeInvalidArgument, fmt.Sprintf("unknown worker state %q", state), nil)
	}
	res, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_workers SET state = ? WHERE worker_id = ? AND removed = 0`, string(state), workerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return core.NewError(core.CodeNotFound, fmt.Sprintf("worker %q does not exist", workerID), map[string]any{"worker": workerID})
	}
	return nil
}

func (s *Store) authenticate(ctx context.Context, workerID, credential string) (Worker, error) {
	w, err := s.Worker(ctx, workerID)
	if err != nil {
		return Worker{}, err
	}
	var hash, previous string
	var removed int
	var expires, previousExpires int64
	if err := s.db.Execer().QueryRow(ctx,
		`SELECT credential_hash, previous_credential_hash, removed, credential_expires_unix_ms,
		        previous_credential_expires_unix_ms
		 FROM worker_workers WHERE worker_id = ?`, workerID).
		Scan(&hash, &previous, &removed, &expires, &previousExpires); err != nil {
		return Worker{}, err
	}
	now := unixms(s.now())
	given := hashToken(credential)
	current := hash != "" && expires > now && subtle.ConstantTimeCompare([]byte(hash), []byte(given)) == 1
	// A rotated credential's predecessor stays valid until its own expiry so
	// a heartbeat response lost on the wire does not lock the worker out.
	prior := previous != "" && previousExpires > now && subtle.ConstantTimeCompare([]byte(previous), []byte(given)) == 1
	if removed != 0 || !current && !prior {
		return Worker{}, core.NewError(core.CodeUnauthenticated, "worker credential is not valid", nil)
	}
	// A live worker's credential slides forward on use; only an idle one
	// expires. Heartbeats rotate it (ADR-0012 §10).
	if current {
		if _, err := s.db.Execer().Exec(ctx,
			`UPDATE worker_workers SET credential_expires_unix_ms = ? WHERE worker_id = ?`,
			unixms(s.now().Add(s.credentialTTL())), workerID); err != nil {
			return Worker{}, err
		}
	}
	return w, nil
}

// touchWorker records a lease or heartbeat as the worker's last sign of life
// and makes it present again after a missed-heartbeat deadline. A worker that
// was paused or is draining stays in that state.
func (s *Store) touchWorker(ctx context.Context, workerID string) error {
	_, err := s.db.Execer().Exec(ctx,
		`UPDATE worker_workers SET heartbeat_ms = ?, present = CASE WHEN removed = 0 THEN 1 ELSE present END
		 WHERE worker_id = ?`, unixms(s.now()), workerID)
	return err
}

// absentWorker is one worker the sweep found past its presence deadline.
type absentWorker struct {
	ID     string
	Pool   string
	Tenant core.TenantID
}

// expireWorkersTx marks workers absent once they have missed presenceMisses
// heartbeats and hold no live lease. A crashed worker must not stay online,
// block a descriptor rollout or count as capacity (ADR-0012 §2, §4).
func (s *Store) expireWorkersTx(ctx context.Context, tx *psql.Execer, now time.Time) ([]absentWorker, error) {
	cutoff := unixms(now.Add(-presenceMisses * s.heartbeat))
	rows, err := tx.Query(ctx,
		`SELECT w.worker_id, w.pool_id, p.tenant FROM worker_workers w
		 JOIN worker_pools p ON p.pool_id = w.pool_id
		 WHERE w.present = 1 AND w.removed = 0 AND w.heartbeat_ms > 0 AND w.heartbeat_ms <= ?
		   AND NOT EXISTS (SELECT 1 FROM worker_invocations i
		                   WHERE i.worker_id = w.worker_id AND i.state IN (?, ?)
		                     AND (i.lease_expires_unix_ms = 0 OR i.lease_expires_unix_ms > ?))`,
		cutoff, stateLeased, stateSpilling, unixms(now))
	if err != nil {
		return nil, err
	}
	var gone []absentWorker
	for rows.Next() {
		var w absentWorker
		var tenant string
		if err := rows.Scan(&w.ID, &w.Pool, &tenant); err != nil {
			_ = rows.Close()
			return nil, err
		}
		w.Tenant = core.TenantID(tenant)
		gone = append(gone, w)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for _, w := range gone {
		if _, err := tx.Exec(ctx,
			`UPDATE worker_workers SET present = 0 WHERE worker_id = ?`, w.ID); err != nil {
			return nil, err
		}
	}
	return gone, nil
}

// Worker reads one worker.
func (s *Store) Worker(ctx context.Context, id string) (Worker, error) {
	var (
		w                   Worker
		state, desc, labels string
		present, removed    int
		lastSeen            int64
	)
	err := s.db.Execer().QueryRow(ctx,
		`SELECT worker_id, pool_id, state, descriptors, labels, capacity, units_used, present, removed, heartbeat_ms
		 FROM worker_workers WHERE worker_id = ?`, id).
		Scan(&w.ID, &w.Pool, &state, &desc, &labels, &w.Capacity, &w.UnitsInUse, &present, &removed, &lastSeen)
	if core.CodeOf(err) == core.CodeNotFound {
		return Worker{}, core.NewError(core.CodeNotFound, fmt.Sprintf("worker %q does not exist", id), map[string]any{"worker": id})
	}
	if err != nil {
		return Worker{}, err
	}
	w.State = State(state)
	w.Present = present != 0
	w.Removed = removed != 0
	if lastSeen > 0 {
		w.LastSeen = fromms(lastSeen)
	}
	if err := json.Unmarshal([]byte(desc), &w.Descriptors); err != nil {
		return Worker{}, core.NewError(core.CodeInternal, "worker: stored descriptors are corrupt",
			map[string]any{"worker": id, "cause": err.Error()})
	}
	if err := json.Unmarshal([]byte(labels), &w.Labels); err != nil {
		return Worker{}, core.NewError(core.CodeInternal, "worker: stored labels are corrupt",
			map[string]any{"worker": id, "cause": err.Error()})
	}
	if w.Labels == nil {
		w.Labels = map[string]string{}
	}
	return w, nil
}

// ListWorkers lists a pool's workers with their state and what they hold.
func (s *Store) ListWorkers(ctx context.Context, poolID string) ([]Worker, error) {
	rows, err := s.db.Execer().Query(ctx,
		`SELECT worker_id FROM worker_workers WHERE pool_id = ? ORDER BY worker_id`, poolID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	out := make([]Worker, 0, len(ids))
	for _, id := range ids {
		w, err := s.Worker(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// SetAgeing changes a pool's ageing period.
func (s *Store) SetAgeing(ctx context.Context, poolID string, d time.Duration) error {
	res, err := s.db.Execer().Exec(ctx, `UPDATE worker_pools SET ageing_ms = ? WHERE pool_id = ?`, d.Milliseconds(), poolID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return core.NewError(core.CodeNotFound, fmt.Sprintf("pool %q does not exist", poolID), map[string]any{"pool": poolID})
	}
	return nil
}

// Leased lists the invocations a worker currently holds.
func (s *Store) Leased(ctx context.Context, workerID string) ([]Invocation, error) {
	rows, err := s.db.Execer().Query(ctx, selectInvocation+` WHERE worker_id = ? AND state = ? ORDER BY client_ref`, workerID, stateLeased)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Invocation
	for rows.Next() {
		inv, err := scanInvocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// SetTenantSettings writes a tenant's weight, concurrency limit and spill-over
// settings for one pool.
func (s *Store) SetTenantSettings(ctx context.Context, poolID string, tenant core.TenantID, ts TenantSettings) error {
	if ts.Weight <= 0 {
		ts.Weight = 1
	}
	_, err := s.db.Execer().Exec(ctx,
		`INSERT INTO worker_tenants (pool_id, tenant, weight, concurrency, spill_over, spill_after_ms)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (pool_id, tenant) DO UPDATE SET
		   weight = excluded.weight, concurrency = excluded.concurrency,
		   spill_over = excluded.spill_over, spill_after_ms = excluded.spill_after_ms`,
		poolID, string(tenant), ts.Weight, ts.Concurrency, boolInt(ts.SpillOver), ts.SpillAfter.Milliseconds())
	return err
}

// TenantSettings reads a tenant's settings; the zero value has weight 1 and
// spill-over off.
func (s *Store) TenantSettings(ctx context.Context, poolID string, tenant core.TenantID) (TenantSettings, error) {
	ts := TenantSettings{Weight: 1}
	var spill int
	var after int64
	err := s.db.Execer().QueryRow(ctx,
		`SELECT weight, concurrency, spill_over, spill_after_ms FROM worker_tenants
		 WHERE pool_id = ? AND tenant = ?`, poolID, string(tenant)).
		Scan(&ts.Weight, &ts.Concurrency, &spill, &after)
	if core.CodeOf(err) == core.CodeNotFound {
		return ts, nil
	}
	if err != nil {
		return ts, err
	}
	ts.SpillOver = spill != 0
	ts.SpillAfter = time.Duration(after) * time.Millisecond
	return ts, nil
}

// Availability reports what the catalog shows for a model in a pool. A pool
// keeps the descriptors of workers that left, so it answers offline rather
// than not_found while a worker restarts (REQ-MODEL-013).
func (s *Store) Availability(ctx context.Context, poolID, modelID string) (Availability, error) {
	var raw string
	err := s.db.Execer().QueryRow(ctx,
		`SELECT descriptor FROM worker_pool_models WHERE pool_id = ? AND model = ?`, poolID, modelID).Scan(&raw)
	if core.CodeOf(err) == core.CodeNotFound {
		return "", core.NewError(core.CodeNotFound, fmt.Sprintf("pool %q does not serve %q", poolID, modelID), map[string]any{"pool": poolID, "model": modelID})
	}
	if err != nil {
		return "", err
	}
	workers, err := s.ListWorkers(ctx, poolID)
	if err != nil {
		return "", err
	}
	require := requiredLabels(ctx)
	now := s.now()
	for _, w := range workers {
		if !s.serving(w, now) {
			continue
		}
		if !labelsMatch(w.Labels, require) {
			continue
		}
		for _, d := range w.Descriptors {
			if d.ID == modelID {
				return AvailabilityOnline, nil
			}
		}
	}
	return AvailabilityOffline, nil
}

// Descriptors lists what a pool serves, with availability.
func (s *Store) Descriptors(ctx context.Context, poolID string) ([]Descriptor, error) {
	rows, err := s.db.Execer().Query(ctx,
		`SELECT model, descriptor FROM worker_pool_models WHERE pool_id = ? ORDER BY model`, poolID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Descriptor
	for rows.Next() {
		var model, raw string
		if err := rows.Scan(&model, &raw); err != nil {
			return nil, err
		}
		var d Descriptor
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// descriptorFor returns the pool's descriptor for one model.
func (s *Store) descriptorFor(ctx context.Context, poolID, modelID string) (Descriptor, error) {
	var raw string
	err := s.db.Execer().QueryRow(ctx,
		`SELECT descriptor FROM worker_pool_models WHERE pool_id = ? AND model = ?`, poolID, modelID).Scan(&raw)
	if core.CodeOf(err) == core.CodeNotFound {
		return Descriptor{}, core.NewError(core.CodeNotFound,
			fmt.Sprintf("pool %q does not serve %q", poolID, modelID), map[string]any{"pool": poolID, "model": modelID})
	}
	if err != nil {
		return Descriptor{}, err
	}
	var d Descriptor
	err = json.Unmarshal([]byte(raw), &d)
	return d, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// poolTenant is the tenant a pool belongs to; a shared or unknown pool is
// deployment-level (empty).
func (s *Store) poolTenant(ctx context.Context, poolID string) core.TenantID {
	p, err := s.Pool(ctx, poolID)
	if err != nil {
		return ""
	}
	return p.Tenant
}

// auditUnavailable fails a security decision closed when its audit record
// cannot be written (auth.md §2).
func auditUnavailable(cause error) error {
	return core.NewError(core.CodeInternal, "worker: audit record could not be written",
		map[string]any{"cause": cause.Error()})
}

// auditEventFor records a security decision for tenant. A tenant-scoped
// producer fails closed: its error is returned so the caller can refuse the
// operation (auth.md §2). poolTenant reports the tenant a pool belongs to;
// an unknown or shared pool is deployment-level.
func (s *Store) auditEventFor(ctx context.Context, tenant core.TenantID, action, resource, outcome, reason string) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Record(ctx, platform.AuditEvent{
		Time:     s.now(),
		Tenant:   tenant,
		Action:   action,
		Resource: resource,
		Outcome:  outcome,
		Reason:   reason,
	})
}

// credentialTTL is how long a join credential stays valid without a
// heartbeat. It is longer than the presence deadline so a transient network
// partition does not force a rejoin.
func (s *Store) credentialTTL() time.Duration { return credentialLifetime * s.heartbeat }

// presenceMisses is how many heartbeat intervals a worker may miss before it
// is no longer present (ADR-0012 §4).
const presenceMisses = 3

func presenceStale(lastSeen, now time.Time, heartbeat time.Duration) bool {
	return lastSeen.IsZero() || now.Sub(lastSeen) > presenceMisses*heartbeat
}

// serving reports whether a worker can take work now: present, active and
// heartbeating. Draining, paused and missed workers are not online.
func (s *Store) serving(w Worker, now time.Time) bool {
	if !w.Present || w.Removed || w.State != StateActive {
		return false
	}
	if w.LastSeen.IsZero() {
		return false
	}
	return now.Sub(w.LastSeen) <= presenceMisses*s.heartbeat
}
