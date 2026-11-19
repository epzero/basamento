// Package sim holds in-process simulators for the manifest-driven providers.
//
// The gate never calls a live vendor: contract tests and BDD scenarios mount
// the HTTP simulator behind httptest and drive the CLI simulator as a Runner.
// Both are deterministic: status calls advance one step per call, so no clock
// or sleep is needed.
package sim
