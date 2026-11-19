// Package launcher implements the two launchers of ADR-0012 §12: http, a
// templated HTTP request with a credential reference, and process, a local
// command that starts kit worker (deployment administrators only). Both
// satisfy platform.Launcher; their shared contract is checked by the suite in
// platform/launcher/conformance.
//
// A launcher reports acceptance only. A worker joining its pool is the only
// proof a worker started, so the scaler in model/worker/scale never trusts an
// accepted request as capacity until the worker is present.
package launcher
