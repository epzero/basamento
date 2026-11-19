-- +goose Up
-- The baseline migration. Schema migrations land with the capabilities that own
-- their tables; goose needs at least one migration to open the store.
SELECT 1;

-- +goose Down
SELECT 1;
