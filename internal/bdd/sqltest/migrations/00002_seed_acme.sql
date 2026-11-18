-- +goose Up
INSERT INTO items (id, tenant_id, key, at)
VALUES ('item-seed-acme', 'acme', 'seed', '2026-01-01 00:00:00');

-- +goose Down
DELETE FROM items WHERE id = 'item-seed-acme';