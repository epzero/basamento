-- +goose Up
INSERT INTO items (id, tenant_id, key, at, required, tag)
VALUES ('item-seeded-by-4', 'acme', 'seeded-by-4', '2026-01-01 00:00:00', 'ok', 'ok');

SELECT * FROM goose_force_this_migration_to_fail;

-- +goose Down
DELETE FROM items WHERE id = 'item-seeded-by-4';