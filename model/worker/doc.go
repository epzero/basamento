// Package worker implements the platform side of ADR-0012 §1 to §11: pools,
// the descriptors their workers declare, the SQL queue with its classes,
// fairness and ageing, leases by free capacity, draining and pausing, the
// worker journal, restart-safe redelivery, worker_lost, spill-over, metrics
// and worker administration, and routing by labels.
//
// A worker is `kit worker`: it dials out, joins one pool with a pool token,
// declares what it serves and pulls invocations from the queue. The platform
// decides their order; a worker only says how many units it has free. The
// contract is basamento.worker.v1; this package is the implementation a flow
// reaches through model.Invoker.
package worker
