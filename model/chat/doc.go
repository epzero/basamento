// Package chat adapts an eino chat model to the model.Invoker contract.
//
// One stream is capped at 8 MiB of payload and 262144 chunks; exceeding either
// ends the invocation with vendor_error. Vendor panics at the stream boundary
// are converted to internal failures without ending the process.
//
// One Start drives one vendor stream. Each chunk is normalized into an event
// in the order the vendor produced it: content into a text event, reasoning
// into a reasoning event, tool-call deltas into tool-call events. The result
// carries the concatenated message and the last usage the vendor reported.
// Callers that do not follow events use model.Invoke and get the same result.
// Media outputs (images, audio files) are the media adapters' (WP-L3-04).
//
// Provider construction lives in WP-L3-04; every eino-ext chat model, and the
// official SDKs wrapped by it, satisfies Config.Model.
package chat
