-- +goose Up
-- The worker layer audits joins, leaves, removals and refused joins. These
-- tables are the platform's audit sink (platform/credentials); the embedded
-- kit database includes them so the security path can fail closed.
CREATE TABLE IF NOT EXISTS audit_events (
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

CREATE INDEX IF NOT EXISTS audit_events_tenant_at ON audit_events (tenant_id, at, id);

CREATE TABLE IF NOT EXISTS audit_seq (
    id      INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    next_id BIGINT NOT NULL
);

INSERT INTO audit_seq (id, next_id) VALUES (1, 0) ON CONFLICT (id) DO NOTHING;

-- +goose Down
DROP TABLE audit_seq;
DROP TABLE audit_events;
