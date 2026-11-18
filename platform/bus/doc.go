// Package bus implements [platform.Bus] and [platform.KV] on NATS JetStream.
//
// Two production paths share one [Store] type:
//
//   - Embedded ([OpenEmbedded]): an in-process nats-server with JetStream file
//     storage under dataDir/nats. The server uses DontListen and clients connect
//     with in-process transport only (no TCP listener, no monitoring port).
//     Store directory permissions are 0o700.
//   - Cloud ([OpenCloud]): a TCP connection to an operator-managed cluster
//     (URL schemes nats or tls). The client opens or verifies fixed
//     JetStream assets (stream KIT_BUS, KV buckets KIT_KV and KIT_BUS_META)
//     without reconfiguring existing assets.
//
// [Options] carries [core.Clock] (KV logical expiry per REQ-BUS-018), optional
// [Options.SubBuffer] and [Options.MaxMessageSize] (defaults in constants), and
// [Options.Replicas] used only when this open creates JetStream assets.
//
// Subjects and KV keys are isolated per string; wildcards are rejected.
// [Store.Close] ends every live subscription, releases the client connection and
// shuts down an embedded server. Open calls honour ctx for setup only.
//
// See specs/bus and ADR-0004, ADR-0005. BDD uses internal/bdd/bustest as the
// in-process fake (design.md section 1).
package bus
