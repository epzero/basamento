-- +goose Up
CREATE TABLE lists (
    id        TEXT NOT NULL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    name      TEXT NOT NULL
);

CREATE TABLE items (
    id        TEXT NOT NULL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    key       TEXT NOT NULL,
    list_id   TEXT REFERENCES lists(id),
    at        TIMESTAMP NOT NULL,
    required  TEXT NOT NULL,
    tag       TEXT NOT NULL CHECK (tag = 'ok'),
    UNIQUE (tenant_id, key)
);

CREATE INDEX items_tenant_key ON items (tenant_id, key);

-- +goose Down
DROP TABLE items;
DROP TABLE lists;