-- +goose Up
-- Durable event log and outbox of a run (WP-L6-01, REQ-STRM-008 … 019).
-- Tables are prefixed because every capability shares one SQL database.
CREATE TABLE eventlog_runs (
    tenant_id  TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    state      TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    PRIMARY KEY (tenant_id, run_id)
);

CREATE TABLE eventlog_events (
    tenant_id  TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    seq        BIGINT NOT NULL,
    type       TEXT NOT NULL,
    v          INTEGER NOT NULL,
    data       TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    PRIMARY KEY (tenant_id, run_id, seq),
    FOREIGN KEY (tenant_id, run_id) REFERENCES eventlog_runs (tenant_id, run_id)
);

CREATE TABLE eventlog_outbox (
    tenant_id  TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    seq        BIGINT NOT NULL,
    relayed_at TIMESTAMP,
    PRIMARY KEY (tenant_id, run_id, seq),
    FOREIGN KEY (tenant_id, run_id, seq) REFERENCES eventlog_events (tenant_id, run_id, seq)
);

CREATE INDEX eventlog_outbox_pending ON eventlog_outbox (tenant_id, run_id, seq)
    WHERE relayed_at IS NULL;

-- +goose Down
DROP TABLE eventlog_outbox;
DROP TABLE eventlog_events;
DROP TABLE eventlog_runs;
