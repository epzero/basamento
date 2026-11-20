// Package telemtest is the deterministic OTLP collector used by the BDD
// telemetry scenarios. It never reaches a vendor: it receives OTLP/HTTP
// protobuf batches in process and records what arrived.
package telemtest
