@wp:WP-L4-01 @level:component
Feature: Compiling flows

  @REQ-FLOW-003 @id:FLOW-003-01
  Scenario: An unguarded cycle is rejected
    Given a flow where node "repair" routes back to node "verify" and neither has a guard
    When the flow is compiled
    Then compilation fails naming node "repair" and the missing guard

  @REQ-FLOW-004 @id:FLOW-004-01
  Scenario: A condition that does not type-check is rejected
    Given a flow whose state schema declares "failed" as an integer
    And an edge with condition "state.failed == 'none'"
    When the flow is compiled
    Then compilation fails naming the edge and the expression
