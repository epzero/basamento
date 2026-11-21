// Package temporal is the Temporal-backed durable runtime (WP-L5-02).
//
// This change delivers the data path: a context-aware payload data converter
// (tenant encryption and blob claim-check), the flow-host workflow with
// DurableContext (Context, Now, NewID, Rand, Step, Sleep, Select, Capabilities),
// and the step activity with exponential backoff retries. Child, Await, Emit and
// Checkpoint return code unsupported until the next changes of WP-L5-02.
//
// Failure messages in Temporal history are plaintext: only core error codes and
// messages cross the boundary (Details maps are dropped). Non-core errors become
// internal with a fixed message; the original text is logged only, bounded like
// panic values. The tenant header is plaintext by necessity. The workflow-bound
// converter fails transient codec errors as workflow tasks (sanitized panic);
// permanent errors are returned. On the workflow-bound path, cancelled means bound
// expiry (the blob layer may map a deadline that way) or store-reported
// cancellation—retried like unavailable, not a caller cancel. Run cancellation
// (REQ-RT-013) is separate. Workflow-bound I/O is capped at 30 s per call so a
// hung blob or key request cannot block a worker thread forever; if that bound
// expires the workflow task fails (sanitized panic) and the server retries, and the
// run outcome is unchanged once I/O succeeds. Client and activity converters treat
// caller-cancelled contexts as permanent errors.
//
// Claim-checked blobs are content-addressed and immutable; replay or activity retry
// may write additional ciphertext blobs with no garbage collection in this phase.
//
// The composition root uses Options and New; New wraps the payload codec in
// workflow.DataConverterWithoutDeadlockDetection for workflow-safe blob I/O.
// (*Runtime).Register wires workers; (*Runtime).DataConverter shares the
// wrapped converter with clients and workers. Tests and BDD wrap
// DataConverter to model tenant-aware clients and record history (not in prod).
//
// Status: in progress (WP-L5-02, EP0-31); embedded server packaging is WP-L5-03.
package temporal
