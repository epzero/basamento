// Package schema generates JSON Schema from Go types, compiles and validates
// instances, and produces RFC 8785 canonical JSON for content-addressing.
//
// Generation maps []byte and named types over []byte to nullable strings with
// contentEncoding "base64". json.RawMessage accepts any JSON value including null.
// Fixed-length [N]byte arrays remain JSON arrays of integers.
//
// Untrusted JSON is bounded: nesting depth at most 10000; number literals at
// most 1000 mantissa digits and exponent magnitude at most 8192; canonicalization
// still enforces exact IEEE-754 doubles.
package schema
