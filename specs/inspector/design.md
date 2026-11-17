# Inspector: design notes

## Open questions

- **Resolving an already-resolved gate (`WP-L6-03`):** Neither `specs/flow-engine` (`REQ-FLOW-006`) nor the API work package (`WP-L6-03`) specifies the error code (e.g., `conflict`) or stream/UI reconciliation behavior when a principal submits a resolution for a gate that another principal has already resolved. Left to `WP-L6-03` (`api`) to specify first.
