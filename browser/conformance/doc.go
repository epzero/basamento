// Package conformance is the browser runner conformance suite: the Go form of
// the contract in contracts/basamento/browser/v1 and the checks every browser
// runner implementation must pass (ADR-0009, specs/browser).
//
// A runner implements Runner. The suite drives it through scripted and
// goal-driven sessions, reads their events, results and artifacts, and
// asserts the observable contract: a session is an invocation, its progress,
// actions and step results stream in order, evidence (the rrweb recording and
// screenshots) lines up with those events, input values stay masked, a
// failing step carries evidence, a goal-driven session stops at its budget,
// and every model call reaches the platform endpoint with the session's own
// token.
//
// The fixture apps (Fixture), their features (Feature) and the scripted
// platform endpoint (NewScriptedModel) are deterministic: no live browser, no
// vendor and no wall clock. browsertest holds a simulated runner that passes
// the suite; the suite's own tests run against it.
package conformance
