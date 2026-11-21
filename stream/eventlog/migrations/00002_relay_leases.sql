-- +goose Up
-- One relay pass owns a run's outbox until its lease expires (WP-L6-01,
-- REQ-STRM-013). A pass over another Log or process skips a run whose lease is
-- still held, so one run's entries reach its subject in sequence order.
CREATE TABLE eventlog_relay_leases (
    tenant_id  TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    PRIMARY KEY (tenant_id, run_id)
);

-- +goose Down
DROP TABLE eventlog_relay_leases;
