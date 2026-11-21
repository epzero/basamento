// Package temporal is the Temporal-backed durable runtime (WP-L5-02).
//
// This change delivers the data path: a context-aware payload data converter
// (tenant encryption and blob claim-check), the flow-host workflow with
// DurableContext (Context, Now, NewID, Rand, Step, Sleep, Select, Capabilities),
// and the step activity with exponential backoff retries. Child, Await, Emit and
// Checkpoint return code unsupported until the next changes of WP-L5-02.
//
// The composition root uses Options and New; (*Runtime).Register wires workers;
// (*Runtime).DataConverter shares the codec with clients. Tests and BDD wrap
// DataConverter to model tenant-aware clients and record history (not in prod).
//
// Status: in progress (WP-L5-02, EP0-31); embedded server packaging is WP-L5-03.
package temporal
