@wp:WP-L4-02 @runtime:any @level:component
Feature: Routing and merging in the flow machine

  @REQ-FLOW-001 @id:FLOW-001-01
  Scenario Outline: The first matching edge wins
    Given flow "triage" routes node "verify" to "review" when "state.failed == 0", to "repair" when "state.failed > 0", otherwise to "fail"
    When node "verify" completes with state failed = <failed>
    Then the next node is "<next>"

    Examples:
      | failed | next   |
      | 0      | review |
      | 2      | repair |
      | -1     | fail   |

  @REQ-FLOW-002 @id:FLOW-002-01
  Scenario: Parallel updates merge the same way in any completion order
    Given flow "fanout" runs nodes "a", "b" and "c" in parallel, each appending its name to "state.log"
    When the nodes complete in order "c", "a", "b"
    Then "state.log" is ["a", "b", "c"]
    And the snapshot equals the snapshot for completion order "a", "b", "c"
