@planned @wp:WP-L3-03 @runtime:any @level:component
Feature: Starting paid invocations safely
  Video and image vendors charge per start. A retry must never cause a second
  charge, and an unknown outcome is resolved before anything is sent again.

  @REQ-MODEL-004 @cost @id:MODEL-004-01
  Scenario: A retried start reuses the client ref
    Given a simulated video provider that rejects the first start with a transient error before accepting
    When a run invokes "video.generate" on it
    Then the provider accepted 1 invocation across 2 start attempts
    And both start attempts carried the same client ref

  @REQ-MODEL-005 @cost @id:MODEL-005-01
  Scenario: An ambiguous start is resolved by lookup
    Given a simulated video provider that accepts the invocation but drops the start response
    And the provider supports lookup by client ref
    When a run invokes "video.generate" on it
    Then the model node finds the invocation by lookup
    And the provider accepted 1 invocation

  @REQ-MODEL-005 @cost @id:MODEL-005-02
  Scenario: An ambiguous start without lookup opens a gate
    Given a simulated video provider that accepts the invocation but drops the start response
    And the provider does not support lookup
    When a run invokes "video.generate" on it
    Then the run waits at gate "ambiguous_start"
    And the provider accepted 1 invocation

  @REQ-MODEL-005 @cost @id:MODEL-005-03
  Scenario: An ambiguous start whose lookup finds nothing opens a gate
    Given a simulated video provider that accepts the invocation but drops the start response
    And the provider supports lookup by client ref
    And the invocation's retention window passes before the model node looks it up
    When a run invokes "video.generate" on it
    Then the run waits at gate "ambiguous_start"
    And the provider accepted 1 invocation

  @REQ-MODEL-010 @id:MODEL-010-01
  Scenario: Invalid parameters fail before the start
    Given a model "sim-video" whose parameter schema requires "duration_s" between 1 and 10
    When a run invokes "video.generate" with duration_s 30
    Then the node fails with code "invalid_argument"
    And the provider accepted 0 invocations
