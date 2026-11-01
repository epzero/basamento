# Review guide: runtimes

High risk: a mistake here loses runs or replays them differently.

- Flow code touches only `DurableContext`: no `time.Now`, global `rand`,
  goroutines, map iteration where order matters, or I/O outside steps.
- A change to workflow code that already ran is versioned (Temporal patching)
  and comes with a replay test over a recorded history.
- Steps are idempotent per `IdempotencyKey`; long or streaming steps send
  heartbeats; cancellation is honored.
- Payloads above a few kilobytes go to blob storage by reference.
- `Caps` tells the truth: no runtime pretends to support what it cannot.
- Embedded server settings change only with a fresh run of
  `runtime/temporal/embedded/bench` and an updated `BENCHMARKS.md`.
