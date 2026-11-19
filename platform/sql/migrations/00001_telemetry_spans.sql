-- +goose Up
CREATE TABLE IF NOT EXISTS telemetry_spans (
	tenant_id TEXT NOT NULL,
	run_id TEXT NOT NULL,
	span_id TEXT NOT NULL,
	trace_id TEXT NOT NULL,
	parent_span_id TEXT NOT NULL,
	name TEXT NOT NULL,
	start_at BIGINT NOT NULL,
	end_at BIGINT NOT NULL,
	status TEXT NOT NULL,
	attrs TEXT NOT NULL,
	PRIMARY KEY (tenant_id, span_id)
);

CREATE INDEX IF NOT EXISTS telemetry_spans_run
	ON telemetry_spans (run_id, tenant_id, start_at, span_id);

-- +goose Down
DROP INDEX IF EXISTS telemetry_spans_run;

DROP TABLE IF EXISTS telemetry_spans;
