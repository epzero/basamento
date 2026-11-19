// Package bus implements [platform.Bus] and [platform.KV] on NATS JetStream.
//
// Two production paths share one [Store] type:
//
//   - Embedded ([OpenEmbedded]): an in-process nats-server with JetStream file
//     storage under dataDir/nats. The server uses DontListen and clients connect
//     with in-process transport only (no TCP listener, no monitoring port).
//     Store directory permissions are 0o700.
//   - Cloud ([OpenCloud]): a TCP connection to an operator-managed cluster
//     (URL schemes nats or tls). Userinfo on scheme nats is accepted only for
//     loopback hosts; credentials on remote hosts require tls://. The client opens
//     or verifies fixed JetStream assets (stream KIT_BUS, KV buckets KIT_KV and
//     KIT_BUS_META) without reconfiguring existing assets.
//
// [Options] carries [core.Clock] (KV logical expiry per REQ-BUS-018), optional
// [Options.SubBuffer] and [Options.MaxMessageSize] (defaults in constants), and
// [Options.Replicas] used when this open creates JetStream assets.
//
// Effective publish and KV value caps follow specs/bus/design.md §5: the smaller
// of the configured maximum and the server max_payload minus JetStream CAS header
// overhead (71 bytes), with an eight-byte deadline prefix on KV records. Open fails
// with invalid_argument when max_payload cannot carry those headers and one byte.
//
// Subscriptions replay retained messages up to the last stream sequence at
// Subscribe time with back-pressure; later live messages use the non-blocking
// buffer and end the subscription on overflow (REQ-BUS-010).
//
// Subjects and KV keys are isolated per string; wildcards are rejected.
// [Store.Close] ends every live subscription, releases the client connection and
// shuts down an embedded server. Open calls honour ctx for setup only.
//
// See specs/bus and ADR-0004, ADR-0005. BDD uses internal/bdd/bustest as the
// in-process fake (design.md section 1).
package bus
