package authz

import (
	"context"
	"database/sql"

	"github.com/epzero/basamento/core"
	psql "github.com/epzero/basamento/platform/sql"
)

// BindingRow is one row from the binding store used during a check.
type BindingRow struct {
	Project   string
	Role      Role
	Condition sql.NullString
}

// SQLStore persists role bindings in authz_bindings.
type SQLStore struct {
	store *psql.Store
}

// NewSQLStore returns a store over an opened platform/sql Store with migrations applied.
func NewSQLStore(store *psql.Store) *SQLStore {
	return &SQLStore{store: store}
}

func requireTenant(ctx context.Context) (core.TenantID, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return "", core.NewError(core.CodeInvalidArgument, "tenant is required", nil)
	}
	return tenant, nil
}

// Grant inserts or updates a binding for the context tenant.
func (s *SQLStore) Grant(ctx context.Context, b Binding) error {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return err
	}
	var cond any
	if b.Condition != "" {
		cond = b.Condition
	}
	_, err = s.store.Execer().Exec(ctx, `
INSERT INTO authz_bindings (tenant, project, principal, role, condition)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (tenant, project, principal, role) DO UPDATE SET
  condition = excluded.condition`,
		string(tenant), b.Project, b.Principal, string(b.Role), cond)
	return err
}

// Revoke removes a binding for the context tenant.
func (s *SQLStore) Revoke(ctx context.Context, principalID string, role Role, project string) error {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return err
	}
	result, err := s.store.Execer().Exec(ctx, `
DELETE FROM authz_bindings
WHERE tenant = ? AND project = ? AND principal = ? AND role = ?`,
		string(tenant), project, principalID, string(role))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return psql.MapError(err)
	}
	if n == 0 {
		return core.NewError(core.CodeNotFound, "binding not found", nil)
	}
	return nil
}

// List returns bindings for principalID scoped to project query rules of REQ-AUTH-010.
func (s *SQLStore) List(ctx context.Context, principalID, project string) ([]Binding, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.queryBindings(ctx, tenant, principalID, project)
	if err != nil {
		return nil, err
	}
	var out []Binding
	for _, r := range rows {
		b := Binding{
			Principal: principalID,
			Role:      r.Role,
			Project:   r.Project,
		}
		if r.Condition.Valid {
			b.Condition = r.Condition.String
		}
		out = append(out, b)
	}
	return out, nil
}

// BindingsForCheck returns bindings of principalID applicable to resourceProject.
func (s *SQLStore) BindingsForCheck(ctx context.Context, principalID, resourceProject string) ([]BindingRow, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	return s.queryBindings(ctx, tenant, principalID, resourceProject)
}

func (s *SQLStore) queryBindings(ctx context.Context, tenant core.TenantID, principalID, resourceProject string) ([]BindingRow, error) {
	var rows *sql.Rows
	var err error
	if resourceProject == "" {
		rows, err = s.store.Execer().Query(ctx, `
SELECT project, role, condition
FROM authz_bindings
WHERE tenant = ? AND principal = ? AND project = ''`,
			string(tenant), principalID)
	} else {
		rows, err = s.store.Execer().Query(ctx, `
SELECT project, role, condition
FROM authz_bindings
WHERE tenant = ? AND principal = ? AND (project = '' OR project = ?)`,
			string(tenant), principalID, resourceProject)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []BindingRow
	for rows.Next() {
		var r BindingRow
		if err := rows.Scan(&r.Project, &r.Role, &r.Condition); err != nil {
			return nil, psql.MapError(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, psql.MapError(err)
	}
	return out, nil
}
