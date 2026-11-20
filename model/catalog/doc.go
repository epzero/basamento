// Package catalog holds the descriptors that a model id stands for and the
// price table that says what a call of it costs.
//
// Prices come from three layers, most specific first: a tenant's override, the
// deployment's override, and the snapshot shipped with the binary. The first
// layer with an entry for a model supplies that model's whole price; layers
// never merge or fall through (REQ-MODEL-071, REQ-MODEL-073). A call's tenant
// is read from its context with core.TenantFrom, never from the request
// (REQ-MODEL-072).
//
// A price entry is either ordered rate rules or a CEL formula. Rate rules are
// chosen by their when condition, which sees the call's start time, parameters,
// usage and tenant. A model with rules and no formula of its own uses the
// default token formula; a model with its own formula uses it with the chosen
// rule's rates. Amounts are whole milli-points, rounded half away from zero.
// A model's own formula must return a whole number of milli-points in integer
// arithmetic; rates and priced amounts are never negative.
package catalog
