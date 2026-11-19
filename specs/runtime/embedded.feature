@wp:WP-L5-03 @level:system
Feature: Embedded profile

  @REQ-RT-003 @profile:embedded @runtime:temporal @id:RT-003-01
  Scenario: The embedded profile needs no external service
    Given a machine with no database, Temporal or NATS server running
    When "kit serve --profile embedded" starts with an empty data directory
    Then the server reports ready
    And a run of conformance flow "linear" completes
