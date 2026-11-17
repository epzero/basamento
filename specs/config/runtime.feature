@planned @wp:WP-L1-05 @level:system
Feature: The runtime kit serve selects
  The runtime is a port like the others. The embedded profile runs flows
  in-process with the `local` runtime when `runtime.url` is unset; the
  embedded Temporal server's behavior belongs to `specs/runtime`.

  @REQ-CONFIG-025 @profile:embedded @id:CONFIG-025-01
  Scenario: The embedded profile runs on the local runtime
    Given an empty data directory and a free loopback listener
    When "kit serve --profile embedded" starts
    Then it reports ready with runtime "local"
