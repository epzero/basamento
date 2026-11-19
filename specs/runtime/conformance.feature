@wp:WP-L5-04
Feature: Runtime conformance

  @REQ-RT-001 @level:component @id:RT-001-01
  Scenario Outline: The conformance flows behave the same on every runtime
    Given the runtime is "<runtime>"
    When conformance flow "<flow>" runs with scripted steps
    Then the final state and durable events equal the golden record for "<flow>"

    Examples:
      | runtime  | flow             |
      | local    | linear           |
      | local    | parallel-merge   |
      | local    | gate-and-timeout |
      | temporal | linear           |
      | temporal | parallel-merge   |
      | temporal | gate-and-timeout |

  @REQ-RT-002 @runtime:temporal @level:system @id:RT-002-01
  Scenario: A run resumes after the process restarts
    Given a run of conformance flow "linear" has completed step 2 of 4
    When the kit process is killed and started again
    Then the run completes
    And steps 1 and 2 were executed once each

  @REQ-RT-004 @runtime:temporal @level:component @id:RT-004-01
  Scenario: Replaying a history against changed flow code reports the divergence
    Given a recorded history of conformance flow "linear"
    When it is replayed against flow code that schedules a different step at event 7
    Then the replay fails with code "determinism_violation" naming event 7
