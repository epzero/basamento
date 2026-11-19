-- +goose Up
ALTER TABLE items ADD COLUMN note TEXT;

-- +goose Down
ALTER TABLE items DROP COLUMN note;