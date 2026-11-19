package credentials

import (
	"context"

	"github.com/epzero/basamento/core"
	psql "github.com/epzero/basamento/platform/sql"
)

// SQLBackend stores encrypted credential rows in the credentials table of a
// SQL store (WP-L1-01). It never sees a plaintext value.
type SQLBackend struct {
	store *psql.Store
}

// NewSQLBackend returns a backend over store.
func NewSQLBackend(store *psql.Store) *SQLBackend {
	return &SQLBackend{store: store}
}

var _ Backend = (*SQLBackend)(nil)

// Upsert inserts r or replaces the ciphertext and updated_at of an existing
// (tenant, name) row, keeping created_at.
func (b *SQLBackend) Upsert(ctx context.Context, r Record) error {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return err
	}
	_, err = b.store.Execer().Exec(ctx, `
INSERT INTO credentials (tenant_id, name, ciphertext, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, name) DO UPDATE SET
  ciphertext = excluded.ciphertext,
  updated_at = excluded.updated_at`,
		string(tenant), r.Name, r.Ciphertext,
		psql.TruncateMicrosecond(r.CreatedAt), psql.TruncateMicrosecond(r.UpdatedAt))
	return err
}

// Get returns the tenant's row for name, or code not_found when absent.
func (b *SQLBackend) Get(ctx context.Context, name string) (Record, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return Record{}, err
	}
	var r Record
	err = b.store.Execer().QueryRow(ctx, `
SELECT name, ciphertext, created_at, updated_at
FROM credentials
WHERE tenant_id = ? AND name = ?`,
		string(tenant), name).Scan(&r.Name, &r.Ciphertext, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return Record{}, missingAsNotFound(err)
	}
	return r, nil
}

// Delete removes the tenant's row for name, or fails with code not_found.
func (b *SQLBackend) Delete(ctx context.Context, name string) error {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return err
	}
	result, err := b.store.Execer().Exec(ctx,
		`DELETE FROM credentials WHERE tenant_id = ? AND name = ?`, string(tenant), name)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errNotFound()
	}
	return nil
}

// List returns every row of the tenant in any order.
func (b *SQLBackend) List(ctx context.Context) ([]Record, error) {
	tenant, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := b.store.Execer().Query(ctx, `
SELECT name, ciphertext, created_at, updated_at
FROM credentials
WHERE tenant_id = ?`, string(tenant))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.Name, &r.Ciphertext, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func missingAsNotFound(err error) error {
	if core.CodeOf(err) == core.CodeNotFound {
		return errNotFound()
	}
	return err
}
