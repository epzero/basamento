// Package structured shapes the output of a chat call into JSON that satisfies
// an output schema. A call climbs the ladder native schema, JSON mode, forced
// tool call on one attempt budget, and repairs the JSON locally after the last
// supported rung (REQ-MODEL-043 to REQ-MODEL-047, REQ-MODEL-065, ADR-0006).
//
// The package owns the rungs, the request each rung sends, schema validation
// through core/schema, deterministic local repair and the per-attempt client
// refs of REQ-MODEL-054. Attempt accounting, retries and timeouts live in
// model/middleware.
package structured
