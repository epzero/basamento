// Package temporal is the Temporal Runtime: the generic FlowHost workflow, step
// activities, a payload codec that stores large payloads in blob storage and
// encrypts them, and (in a separate Go module, to keep server dependencies out
// of library consumers) the embedded server modeled on temporalio/cli's
// internal/devserver with file-backed SQLite.
//
// Status: planned (WP-L5-02, WP-L5-03).
package temporal
