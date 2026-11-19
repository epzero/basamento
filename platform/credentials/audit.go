package credentials

import (
	"context"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	psql "github.com/epzero/basamento/platform/sql"
)

// AuditStore is the SQL implementation of platform.Audit: one append-only
// table shared by every producer, holding tenant events and deployment-level
// events (REQ-CRED-029 to REQ-CRED-035). The composition root passes it to
// Store and to other producers; there is no second audit store.
type AuditStore struct {
	store *psql.Store
}

// NewSQLAudit returns an audit store over store.
func NewSQLAudit(store *psql.Store) *AuditStore {
	return &AuditStore{store: store}
}

var _ platform.Audit = (*AuditStore)(nil)

// Record appends e for the effective tenant derived from ctx, and never
// updates or deletes a stored row (REQ-CRED-030). With a tenant t in ctx,
// e.Tenant must be empty or t; without a tenant, e.Tenant must be empty. A
// disagreement is rejected with code invalid_argument and writes nothing
// (REQ-CRED-034). An event with no tenant is a deployment-level row (tenant_id
// empty) that tenant reads never return (REQ-CRED-035).
func (a *AuditStore) Record(ctx context.Context, e platform.AuditEvent) error {
	tenant, err := auditTenant(ctx, e.Tenant)
	if err != nil {
		return err
	}
	id, err := a.nextID(ctx)
	if err != nil {
		return err
	}
	_, err = a.store.Execer().Exec(ctx, `
INSERT INTO audit_events
  (id, tenant_id, at, principal, action, resource, outcome, run_id, step, reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(tenant), psql.TruncateMicrosecond(e.Time), e.Principal, e.Action,
		e.Resource, e.Outcome, e.RunID, e.Step, e.Reason)
	return err
}

// Read returns the calling tenant's events, ordered by time then id, and never
// deployment-level rows. A context without a tenant fails with code
// invalid_argument (REQ-CRED-031, REQ-CRED-033, REQ-CRED-035). It is the
// package's inspection API, not part of platform.Audit.
func (a *AuditStore) Read(ctx context.Context) ([]platform.AuditEvent, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := a.store.Execer().Query(ctx, `
SELECT tenant_id, at, principal, action, resource, outcome, run_id, step, reason
FROM audit_events
WHERE tenant_id = ?
ORDER BY at, id`, string(tenant))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []platform.AuditEvent
	for rows.Next() {
		var (
			tenantID string
			event    platform.AuditEvent
		)
		if err := rows.Scan(&tenantID, &event.Time, &event.Principal, &event.Action,
			&event.Resource, &event.Outcome, &event.RunID, &event.Step, &event.Reason); err != nil {
			return nil, psql.MapError(err)
		}
		event.Tenant = core.TenantID(tenantID)
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, psql.MapError(err)
	}
	return out, nil
}

// nextID takes the next value of the single audit sequence row. The UPDATE is
// atomic on both dialects, so concurrent producers get distinct ids without a
// transaction; a failed insert may leave a gap, which ordering tolerates.
func (a *AuditStore) nextID(ctx context.Context) (int64, error) {
	var id int64
	err := a.store.Execer().QueryRow(ctx,
		`UPDATE audit_seq SET next_id = next_id + 1 WHERE id = 1 RETURNING next_id`).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func auditTenant(ctx context.Context, claimed core.TenantID) (core.TenantID, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		if claimed != "" {
			return "", errInvalidAuditTenant()
		}
		return "", nil
	}
	if claimed != "" && claimed != tenant {
		return "", errInvalidAuditTenant()
	}
	return tenant, nil
}

func errInvalidAuditTenant() error {
	return core.NewError(core.CodeInvalidArgument, "invalid audit tenant", nil)
}
