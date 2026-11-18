@planned @wp:WP-L4-01 @level:component
Feature: Reserved edge endpoints

  START and FAIL are the entry and the failure exit of a flow. They are edge
  endpoints, not nodes (owner decision D2, 2026-11-17).

  @REQ-FLOW-039 @id:FLOW-039-01
  Scenario: START and FAIL are accepted as edge endpoints without a node
    Given a flow whose edges are from "START" to "plan", from "plan" to "check", and from "check" to "FAIL"
    When the flow is compiled
    Then compilation succeeds
    And the compiled flow has no node "START" and no node "FAIL"

  @REQ-FLOW-040 @id:FLOW-040-01
  Scenario Outline: A node named START or FAIL is rejected
    Given a flow that declares a node named "<name>"
    When the flow is compiled
    Then compilation fails with code "invalid_argument" naming node "<name>"

    Examples:
      | name  |
      | START |
      | FAIL  |

  @REQ-FLOW-041 @id:FLOW-041-01
  Scenario: An edge into START is rejected
    Given a flow whose edges are from "START" to "plan", from "plan" to "check", and from "check" to "START"
    When the flow is compiled
    Then compilation fails with code "invalid_argument" naming the edge from "check" to "START"

  @REQ-FLOW-042 @id:FLOW-042-01
  Scenario: An edge out of FAIL is rejected
    Given a flow whose edges are from "START" to "plan", from "plan" to "check", and from "FAIL" to "check"
    When the flow is compiled
    Then compilation fails with code "invalid_argument" naming the edge from "FAIL" to "check"

  @REQ-FLOW-043 @id:FLOW-043-01
  Scenario Outline: A flow without exactly one edge from START is rejected
    Given a flow with nodes "plan" and "check" and <count> edges from "START"
    When the flow is compiled
    Then compilation fails with code "invalid_argument" stating that the flow needs exactly one edge from "START"

    Examples:
      | count |
      | 0     |
      | 2     |
