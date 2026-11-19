// Package temporal is the Temporal runtime: the generic FlowHost runs as one
// workflow, each Step as an activity, gates as synchronous updates, timers and
// child workflows through the same DurableContext, and Checkpoint as
// ContinueAsNew. A payload codec encrypts every history payload for the run's
// tenant and stores payloads over a threshold in blob storage. The embedded
// server lives in a separate Go module, runtime/temporal/embedded.
//
// Status: implemented (WP-L5-02); the embedded server is WP-L5-03.
package temporal
