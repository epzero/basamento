package sqltest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
	psql "github.com/epzero/basamento/platform/sql"
)

// SampleRepo is the conformance repository over lists and items.
type SampleRepo struct {
	store   *psql.Store
	dialect string
}

// NewSampleRepo wraps a store.
func NewSampleRepo(store *psql.Store) *SampleRepo {
	return &SampleRepo{store: store, dialect: store.Dialect()}
}

func (r *SampleRepo) requireTenant(ctx context.Context) (core.TenantID, error) {
	id, ok := core.TenantFrom(ctx)
	if !ok {
		return "", core.NewError(core.CodeInvalidArgument, "sql: tenant required", nil)
	}
	return id, nil
}

// StoreItem inserts or replaces an item for the tenant in ctx.
func (r *SampleRepo) StoreItem(ctx context.Context, key string, opts StoreOptions) error {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return err
	}
	if err := validateTextFields(key, opts.Note); err != nil {
		return err
	}
	at := psqlTruncate(opts.At)
	id := opts.ID
	if id == "" {
		id = fmt.Sprintf("item-%s-%s", tenant, key)
	}
	e := r.store.Execer()
	if opts.ListName != "" {
		listID, err := r.ensureList(ctx, string(tenant), opts.ListName)
		if err != nil {
			return err
		}
		_, err = e.Exec(ctx, `INSERT INTO items (id, tenant_id, key, list_id, at, note) VALUES (?, ?, ?, ?, ?, ?)`,
			id, string(tenant), key, listID, at, nullString(opts.Note))
		return err
	}
	_, err = e.Exec(ctx, `INSERT INTO items (id, tenant_id, key, at, note) VALUES (?, ?, ?, ?, ?)`,
		id, string(tenant), key, at, nullString(opts.Note))
	return err
}

// StoreOptions optional fields for StoreItem.
type StoreOptions struct {
	ID       string
	Note     string
	At       time.Time
	ListName string
}

func validateTextFields(fields ...string) error {
	for _, f := range fields {
		if strings.ContainsRune(f, 0) {
			return core.NewError(core.CodeInvalidArgument, "sql: text contains nul", nil)
		}
	}
	return nil
}

func psqlTruncate(t time.Time) time.Time {
	if t.IsZero() {
		t = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return t.UTC().Truncate(time.Microsecond)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (r *SampleRepo) ensureList(ctx context.Context, tenant, name string) (string, error) {
	id := fmt.Sprintf("list-%s-%s", tenant, name)
	e := r.store.Execer()
	_, err := e.Exec(ctx, `INSERT INTO lists (id, tenant_id, name) VALUES (?, ?, ?)
		ON CONFLICT(id) DO NOTHING`, id, tenant, name)
	if err != nil {
		return "", err
	}
	return id, nil
}

// StoreItemInMissingList sets list_id to a nonexistent list.
func (r *SampleRepo) StoreItemInMissingList(ctx context.Context, key string) error {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("item-%s-%s", tenant, key)
	_, err = r.store.Execer().Exec(ctx, `INSERT INTO items (id, tenant_id, key, list_id, at) VALUES (?, ?, ?, ?, ?)`,
		id, string(tenant), key, "list-missing", time.Now().UTC())
	return err
}

// CountItemsByKey counts items with key for tenant.
func (r *SampleRepo) CountItemsByKey(ctx context.Context, tenant, key string) (int, error) {
	var n int
	err := r.store.Execer().QueryRow(ctx, `SELECT COUNT(*) FROM items WHERE tenant_id = ? AND key = ?`, tenant, key).Scan(&n)
	return n, err
}

// HasItemKey reports whether any tenant has the key.
func (r *SampleRepo) HasItemKey(ctx context.Context, key string) (bool, error) {
	var n int
	err := r.store.Execer().QueryRow(ctx, `SELECT COUNT(*) FROM items WHERE key = ?`, key).Scan(&n)
	return n > 0, err
}

// DeleteItemByKey deletes the tenant's item with key.
func (r *SampleRepo) DeleteItemByKey(ctx context.Context, key string) error {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return err
	}
	_, err = r.store.Execer().Exec(ctx, `DELETE FROM items WHERE tenant_id = ? AND key = ?`, string(tenant), key)
	return err
}

// ListItemKeys returns keys for the tenant in byte order.
func (r *SampleRepo) ListItemKeys(ctx context.Context) ([]string, error) {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	order := `key COLLATE BINARY`
	if r.dialect == "postgres" {
		order = `key COLLATE "C"`
	}
	q := fmt.Sprintf(`SELECT key FROM items WHERE tenant_id = ? ORDER BY %s`, order)
	rows, err := r.store.Execer().Query(ctx, q, string(tenant))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// ItemAt returns the stored timestamp for key.
func (r *SampleRepo) ItemAt(ctx context.Context, key string) (time.Time, error) {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return time.Time{}, err
	}
	var at time.Time
	err = r.store.Execer().QueryRow(ctx, `SELECT at FROM items WHERE tenant_id = ? AND key = ?`, string(tenant), key).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, core.NewError(core.CodeNotFound, "sql: not found", nil)
	}
	return at.UTC().Truncate(time.Microsecond), mapRowErr(err)
}

// ItemNote returns the note for key.
func (r *SampleRepo) ItemNote(ctx context.Context, key string) (string, error) {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return "", err
	}
	var note sql.NullString
	err = r.store.Execer().QueryRow(ctx, `SELECT note FROM items WHERE tenant_id = ? AND key = ?`, string(tenant), key).Scan(&note)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.NewError(core.CodeNotFound, "sql: not found", nil)
	}
	if !note.Valid {
		return "", nil
	}
	return note.String, mapRowErr(err)
}

func mapRowErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return core.NewError(core.CodeNotFound, "sql: not found", nil)
	}
	return err
}

// LookupIDByKey finds id for tenant and key (test helper).
func (r *SampleRepo) LookupIDByKey(ctx context.Context, tenant, key string) (string, error) {
	var id string
	err := r.store.Execer().QueryRow(ctx, `SELECT id FROM items WHERE tenant_id = ? AND key = ?`, tenant, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.NewError(core.CodeNotFound, "sql: not found", nil)
	}
	return id, err
}

// GetByID reads by primary key, scoped to tenant.
func (r *SampleRepo) GetByID(ctx context.Context, id string) (string, error) {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return "", err
	}
	var key string
	err = r.store.Execer().QueryRow(ctx, `SELECT key FROM items WHERE id = ? AND tenant_id = ?`, id, string(tenant)).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.NewError(core.CodeNotFound, "sql: not found", nil)
	}
	return key, mapRowErr(err)
}

// UpdateKeyByID changes key for id if row belongs to tenant.
func (r *SampleRepo) UpdateKeyByID(ctx context.Context, id, newKey string) error {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return err
	}
	res, err := r.store.Execer().Exec(ctx, `UPDATE items SET key = ? WHERE id = ? AND tenant_id = ?`, newKey, id, string(tenant))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return core.NewError(core.CodeNotFound, "sql: not found", nil)
	}
	return nil
}

// DeleteByID deletes when tenant matches.
func (r *SampleRepo) DeleteByID(ctx context.Context, id string) error {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return err
	}
	res, err := r.store.Execer().Exec(ctx, `DELETE FROM items WHERE id = ? AND tenant_id = ?`, id, string(tenant))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return core.NewError(core.CodeNotFound, "sql: not found", nil)
	}
	return nil
}

// ItemListName returns the list name an item belongs to, or "" when none.
func (r *SampleRepo) ItemListName(ctx context.Context, tenant, key string) (string, error) {
	var listID sql.NullString
	err := r.store.Execer().QueryRow(ctx, `SELECT list_id FROM items WHERE tenant_id = ? AND key = ?`, tenant, key).Scan(&listID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.NewError(core.CodeNotFound, "sql: not found", nil)
	}
	if err != nil || !listID.Valid {
		return "", mapRowErr(err)
	}
	var name string
	err = r.store.Execer().QueryRow(ctx, `SELECT name FROM lists WHERE id = ? AND tenant_id = ?`, listID.String, tenant).Scan(&name)
	return name, mapRowErr(err)
}

// DeleteList deletes list by name for tenant.
func (r *SampleRepo) DeleteList(ctx context.Context, name string) error {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("list-%s-%s", tenant, name)
	_, err = r.store.Execer().Exec(ctx, `DELETE FROM lists WHERE id = ? AND tenant_id = ?`, id, string(tenant))
	return err
}

// TxStoreItems stores items in a transaction; failAfterSecond forces rollback test.
func (r *SampleRepo) TxStoreItems(ctx context.Context, keys []string, failAfterSecond bool) (err error) {
	tenant, err := r.requireTenant(ctx)
	if err != nil {
		return err
	}
	return r.store.InTx(ctx, func(ctx context.Context, exec *psql.Execer) error {
		for i, key := range keys {
			id := fmt.Sprintf("item-%s-%s-tx", tenant, key)
			if _, err := exec.Exec(ctx, `INSERT INTO items (id, tenant_id, key, at) VALUES (?, ?, ?, ?)`,
				id, string(tenant), key, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
				return err
			}
			if failAfterSecond && i == 1 {
				return core.NewError(core.CodeInternal, "sql: injected transaction failure", nil)
			}
		}
		return nil
	})
}
