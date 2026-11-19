-- +goose Up
CREATE TABLE credentials (
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    ciphertext BYTEA NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    PRIMARY KEY (tenant_id, name)
);

CREATE TABLE audit_events (
    id         BIGINT NOT NULL PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    at         TIMESTAMP NOT NULL,
    principal  TEXT NOT NULL,
    action     TEXT NOT NULL,
    resource   TEXT NOT NULL,
    outcome    TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    step       TEXT NOT NULL,
    reason     TEXT NOT NULL
);

CREATE INDEX audit_events_tenant_at ON audit_events (tenant_id, at, id);

-- One row hands out audit ids. UPDATE ... RETURNING is atomic on both
-- dialects, so producers get distinct ids without a transaction; a gap after
-- a failed insert is harmless because reads order by time then id.
CREATE TABLE audit_seq (
    id      INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    next_id BIGINT NOT NULL
);

INSERT INTO audit_seq (id, next_id) VALUES (1, 0);

-- +goose Down
DROP TABLE audit_seq;
DROP TABLE audit_events;
DROP TABLE credentials;
