@planned @wp:WP-L3-03 @cost @runtime:any @level:component
Feature: Reservation and settlement of invocation costs

  Background:
    Given a model "sim-video" priced at "params.duration_s * 100" milli-points

  @REQ-MODEL-006 @id:MODEL-006-01
  Scenario: A successful invocation charges its actual cost
    Given the run has a budget of 5000 milli-points
    When a run invokes "video.generate" with duration_s 5
    And the provider reports success with cost 480 milli-points
    Then 500 milli-points were reserved before the start
    And the run is charged 480 milli-points and the reservation is closed

  @REQ-MODEL-007 @id:MODEL-007-01
  Scenario: An invocation over budget is not started
    Given the run has a budget of 300 milli-points
    When a run invokes "video.generate" with duration_s 5
    Then the node fails with code "budget_exceeded"
    And the provider accepted 0 invocations

  @REQ-MODEL-009 @id:MODEL-009-01
  Scenario: Cancelling a run releases the reservation when the provider confirms no charge
    Given the run has a budget of 5000 milli-points
    And a run invoked "video.generate" with duration_s 5 and the invocation is still running
    When the run is cancelled
    And the provider confirms cancellation with cost 0
    Then the provider received a cancel request for the invocation
    And the reservation is released

  @REQ-MODEL-009 @id:MODEL-009-02
  Scenario: Cancelling a run settles the actual cost when the provider already charged
    Given the run has a budget of 5000 milli-points
    And a run invoked "video.generate" with duration_s 5 and the invocation is still running
    When the run is cancelled
    And the provider reports cancellation with cost 200 milli-points
    Then the provider received a cancel request for the invocation
    And the run is charged 200 milli-points and the remaining reservation is released
