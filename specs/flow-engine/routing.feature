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

  @REQ-FLOW-047 @id:FLOW-047-01
  Scenario: A visit budget stops its node after the last allowed visit
    Given a flow "loop" where node "verify" of kind "set" precedes node "repair" of kind "set", which routes back to node "verify" and has a guard of 3 visits
    When a run of the flow starts
    Then node "repair" ran 3 times
    And the run fails with code "budget_exceeded" naming node "repair" and the visit budget 3

  @REQ-FLOW-047 @cost @id:FLOW-047-02
  Scenario: A cost budget stops its node before the third visit
    Given a flow "loop" where node "verify" of kind "set" precedes node "repair" of kind "tool", which routes back to node "verify" and has a guard of 80 milli-points
    And each completion of node "repair" carries a settled cost of 40 milli-points
    When a run of the flow starts
    Then node "repair" ran 2 times
    And the run fails with code "budget_exceeded" naming node "repair" and the cost budget 80 milli-points

  @REQ-FLOW-048 @id:FLOW-048-01
  Scenario: A map over more items than the fan-out limit fails before any fan-out
    Given a flow whose node "impl" of kind "map" fans out over "state.items" to node "impl_item" with "max_parallel" 4
    And "state.items" holds 10001 items
    When a run of the flow starts
    Then the run fails with code "budget_exceeded" naming node "impl", 10001 items and the limit 10000
    And the run created no fan-out
    And the failure carries no state data

  @REQ-FLOW-048 @id:FLOW-048-02
  Scenario: A map over items exactly at the fan-out limit fans out every item
    Given a flow whose node "impl" of kind "map" fans out over "state.items" to node "impl_item" with "max_parallel" 4
    And "state.items" holds 10000 items
    When a run of the flow starts
    Then node "impl" fans out to 10000 items of node "impl_item"

  @REQ-FLOW-049 @cost @id:FLOW-049-01
  Scenario: A finished node's settled cost accrues to the run
    Given a flow whose node "a" of kind "tool" precedes node "b" of kind "tool"
    And a run of the flow has started
    When node "a" completes with a settled cost of 40 milli-points
    And node "b" fails with a settled cost of 25 milli-points
    Then the run's settled cost is 65 milli-points

  @REQ-FLOW-049 @cost @id:FLOW-049-02
  Scenario: A later expression reads the accrued cost as run.cost
    Given a flow whose node "a" of kind "tool" routes to node "b" of kind "set" when "run.cost == 40", otherwise to node "c" of kind "set"
    When node "a" completes with a settled cost of 40 milli-points
    Then the next node is "b"
