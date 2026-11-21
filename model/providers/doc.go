// Package providers implements the manifest-driven model providers and the
// model router.
//
// Two generic drivers cover most vendors without code: an HTTP API provider
// (start, status, cancel and lookup endpoints, credentials by reference and
// gojq mappings to and from our request) and a command-line provider (argv
// mapping, a working directory with one subdirectory per invocation, progress
// parsed from stdout and output globs).
// A vendor whose protocol a manifest cannot express gets a purpose-built
// adapter in its own package; every provider ships a simulator and the same
// contract tests (docs/architecture/model-layer.md §3, ADR-0006).
//
// Manifests are data. LoadManifest validates them once; NewHTTP and NewCLI
// compile every mapping before an invocation can reach a vendor, and an
// invalid manifest is a configuration error rather than a failed call.
//
// The router chooses a model by capability and locality. In the cloud profile
// it never selects a machine-local model (REQ-MODEL-011); otherwise a cloud
// model wins over a worker model, which wins over a machine-local one.
//
// The gate never calls a live vendor. Tests drive the HTTP driver against an
// in-process simulator and the CLI driver against a simulated Runner.
package providers
