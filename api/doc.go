// Package api exposes the platform's HTTP surface: the run routes, the native
// run stream over SSE, the AG-UI adapter, authentication and tenant wiring and
// the mapping from platform error codes to HTTP responses.
//
// A request is authenticated first; its principal's tenant becomes the
// request's tenant, and no route takes a tenant from the request itself
// (REQ-AUTH-002). Run reads are served for the request tenant only, and a run of
// another tenant is answered as not_found, the same answer as a run that does
// not exist.
//
// Routes:
//
//	GET  /v1/runs                 list the tenant's runs
//	GET  /v1/runs/{id}            one run
//	POST /v1/runs/{id}/cancel     cancel a run (permission run.cancel)
//	GET  /v1/runs/{id}/stream     the native run stream over SSE
//	GET  /agui?run_id=...         the AG-UI adapter over SSE
//
// Status: implemented (WP-L6-03) except the OpenAI-compatible model endpoint
// and the MCP server, which have no approved scenario and are not served yet.
package api
