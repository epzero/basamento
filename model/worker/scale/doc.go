// Package scale implements ADR-0012 §12: worker templates, their single-use
// join tokens, the scaler that asks a launcher for workers when work waits,
// and the policy by which a launched worker leaves by itself. The Launcher
// port is platform.Launcher; the http and process implementations live in
// platform/launcher.
//
// Templates are stored beside the worker queue so the catalog can list their
// models as on_demand and a launched worker's join token can be spent by the
// same store that admits pool tokens. The scaler never kills a worker: it only
// asks for more, and a worker leaves when idle, after its maximum lifetime or
// after its maximum number of invocations.
package scale
