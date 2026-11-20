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

  @REQ-FLOW-003 @id:FLOW-003-02 @wp:WP-L4-01
  Scenario: A cycle that only a condition guards is rejected
    Given a flow where node "repair" routes back to node "verify" and only "repair" has a guard whose condition is "true"
    When the flow is compiled
    Then compilation fails naming node "repair" and the missing guard

  @REQ-FLOW-003 @id:FLOW-003-03 @wp:WP-L4-01
  Scenario: A cycle bounded by a visit budget is accepted
    Given a flow where node "repair" routes back to node "verify" and only "repair" has a guard of 3 visits
    When the flow is compiled
    Then compilation succeeds

  @REQ-FLOW-003 @id:FLOW-003-04 @wp:WP-L4-01
  Scenario: A cycle bounded by a cost budget is accepted
    Given a flow where node "repair" routes back to node "verify" and only "repair" has a guard of 5000 milli-points
    When the flow is compiled
    Then compilation succeeds

  @REQ-FLOW-045 @id:FLOW-045-01 @wp:WP-L4-01
  Scenario: A cycle bounded at the guard maxima is accepted
    Given a flow where node "repair" routes back to node "verify" and only "repair" has a guard of 1000000 visits and 1000000000 milli-points
    When the flow is compiled
    Then compilation succeeds

  @REQ-FLOW-045 @id:FLOW-045-02 @wp:WP-L4-01
  Scenario Outline: A guard budget outside its range is rejected
    Given a flow where node "repair" routes back to node "verify" and only "repair" has a guard of <value> <unit>
    When the flow is compiled
    Then compilation fails with code "invalid_argument" naming node "repair", the field "<field>" and the maximum <maximum>

    Examples:
      | value      | unit         | field      | maximum    |
      | -1         | visits       | max_visits | 1000000    |
      | -1         | milli-points | cost_mpts  | 1000000000 |
      | 1000001    | visits       | max_visits | 1000000    |
      | 1000000001 | milli-points | cost_mpts  | 1000000000 |

  @REQ-FLOW-046 @id:FLOW-046-01 @wp:WP-L4-01
  Scenario Outline: A definition at a size limit is accepted
    Given a flow whose <measure> is <maximum> <unit>
    When the flow is compiled
    Then compilation succeeds

    Examples:
      | measure         | maximum | unit  |
      | node count      | 4096    | nodes |
      | edge count      | 16384   | edges |
      | expression size | 16384   | bytes |
      | definition size | 1048576 | bytes |

  @REQ-FLOW-046 @id:FLOW-046-02 @wp:WP-L4-01
  Scenario Outline: A definition over a size limit is rejected
    Given a flow whose <measure> is <value> <unit>
    When the flow is compiled
    Then compilation fails with code "invalid_argument" naming the <measure> limit of <maximum> <unit>

    Examples:
      | measure         | value   | unit  | maximum |
      | node count      | 4097    | nodes | 4096    |
      | edge count      | 16385   | edges | 16384   |
      | expression size | 16385   | bytes | 16384   |
      | definition size | 1048577 | bytes | 1048576 |

  @REQ-FLOW-062 @id:FLOW-062-01 @wp:WP-L4-01
  Scenario: A map node whose child is not a declared node is rejected
    Given a flow with a map node "fanout" that fans out over "state.items" to node "ghost"
    When the flow is compiled
    Then compilation fails with code "invalid_argument" naming the map node "fanout" and the missing child node "ghost"

  @REQ-FLOW-063 @id:FLOW-063-01 @wp:WP-L4-01
  Scenario Outline: A map child of a kind the machine runs itself is rejected
    Given a flow with a map node "fanout" that fans out over "state.items" to node "fanout_item"
    And the node "fanout_item" is of kind "<kind>"
    When the flow is compiled
    Then compilation fails with code "invalid_argument" naming the map node "fanout", the child node "fanout_item" and the kind "<kind>"

    Examples:
      | kind     |
      | set      |
      | parallel |
      | map      |

  @REQ-FLOW-064 @id:FLOW-064-01 @wp:WP-L4-01
  Scenario: One child node claimed by two map nodes is rejected
    Given a flow with a map node "fanout" that fans out over "state.items" to node "fanout_item"
    And an edge from node "fanout" to node "review"
    And a map node "review" that fans out over "state.files" to node "fanout_item"
    When the flow is compiled
    Then compilation fails with code "invalid_argument" naming the map nodes "fanout" and "review" and the child node "fanout_item"

  @REQ-FLOW-065 @id:FLOW-065-01 @wp:WP-L4-01
  Scenario: A map child that carries a guard is rejected
    Given a flow with a map node "fanout" that fans out over "state.items" to node "fanout_item"
    And the node "fanout_item" has a guard of 3 visits
    When the flow is compiled
    Then compilation fails with code "invalid_argument" naming the map node "fanout" and the child node "fanout_item"
    And the failure says that a guard counts visits of the nodes the machine routes to while a map child runs once per element

  @REQ-FLOW-066 @id:FLOW-066-01 @wp:WP-L4-01
  Scenario: A condition on the edge from START is rejected
    Given a flow whose edge from "START" to "plan" is conditional on "true"
    When the flow is compiled
    Then compilation fails with code "invalid_argument" naming the edge from "START" to "plan"
