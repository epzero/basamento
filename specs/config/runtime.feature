@planned @wp:WP-L1-05 @level:system
Feature: The runtime kit serve selects
  The runtime is a port like the others. The embedded profile runs flows
  in-process with the `local` runtime unless a URL asks for the embedded
  Temporal server; the server's own behavior belongs to `specs/runtime`.

  @REQ-CONFIG-025 @profile:embedded @id:CONFIG-025-01
  Scenario: The embedded profile runs on the local runtime
    Given an empty data directory and a free loopback listener
    When "kit serve --profile embedded" starts
    Then it reports ready with runtime "local"
    And the run engine is "local"

  @REQ-CONFIG-026 @profile:embedded @id:CONFIG-026-01
  Scenario: The embedded Temporal server is part of readiness
    Given the embedded profile with "temporal-embedded:///data/temporal.db" as "runtime.url"
    And an empty data directory and a free loopback listener
    When "kit serve" starts
    Then it reports ready with runtime "temporal-embedded"
    And the embedded Temporal server is running when it reports ready
