@wp:WP-L2-01 @level:component
Feature: Evaluating expressions deterministically
  A compiled expression is evaluated on JSON values bound to its variables.
  The same expression on the same values gives the same result, or the same
  error, on every evaluation, on every machine and at any time.

  @REQ-EXPR-005 @id:EXPR-005-02
  Scenario: Time enters an expression only through a variable
    Given an expression environment declaring "at" with schema {"type": "string", "format": "date-time"}
    And the clock is frozen at "2026-11-16T08:00:00Z"
    When the expression "at.getHours('Europe/Rome')" is evaluated with "at" bound to "2026-11-16T18:30:00Z"
    And the clock advances by 6 hours
    And the expression "at.getHours('Europe/Rome')" is evaluated again with "at" bound to "2026-11-16T18:30:00Z"
    Then both results are 19

  @REQ-EXPR-006 @id:EXPR-006-01
  Scenario: A comprehension over a bound map visits its keys in ascending order
    Given an expression environment declaring "m" with schema {"type": "object", "additionalProperties": {"type": "integer"}}
    When the expression "m.map(k, k)" is evaluated 100 times with "m" bound to {"c": 3, "a": 1, "b": 2}
    Then every result is ["a", "b", "c"]

  @REQ-EXPR-006 @id:EXPR-006-02
  Scenario: A comprehension over a map literal visits its keys in ascending order
    Given an expression environment declaring no variables
    When the expression "{'delta': 4, 'alpha': 1, 'charlie': 3, 'bravo': 2}.filter(k, k != 'charlie')" is evaluated 100 times
    Then every result is ["alpha", "bravo", "delta"]

  @REQ-EXPR-021 @id:EXPR-021-01
  Scenario: A map with keys of several kinds iterates them in kind order
    Given an expression environment declaring no variables
    When the expression "{'b': 1, 2.5: 2, 3u: 3, -1: 4, true: 5, 'a': 6}.map(k, k)" is evaluated 100 times
    Then every result is [true, -1, 3, 2.5, "a", "b"]

  @REQ-EXPR-021 @id:EXPR-021-02
  Scenario: A map with keys of three kinds sorts each kind by value
    Given an expression environment declaring no variables
    When the expression "{'beta': 1, 'alpha': 2, 10: 3, 9: 4, 1.5: 5}.map(k, k)" is evaluated 100 times
    Then every result is [9, 10, 1.5, "alpha", "beta"]

  @REQ-EXPR-021 @id:EXPR-021-03
  Scenario Outline: A map whose keys are all doubles orders them numerically
    Given an expression environment declaring no variables
    When the expression "<expression>" is evaluated 100 times
    Then every result is <result>

    Examples:
      | expression                                        | result                      |
      | {1.5: 'a', -2.5: 'd', 0.0: 'b'}.map(k, k)         | [-2.5, 0, 1.5]              |
      | {1.5: 'a', -5.0 / 0.0: 'd', 0.0: 'b', 5.0 / 0.0: 'e'}.map(k, string(k)) | ["-Inf", "0", "1.5", "+Inf"] |

  @REQ-EXPR-021 @id:EXPR-021-04
  Scenario: A map whose keys include a NaN orders the NaN after every other number
    Given an expression environment declaring no variables
    When the expression "[{1.5: 1, 0.0 / 0.0: 2, -2.5: 3}.map(k, string(k)), {1.5: 1, 0.0 / 0.0: 2, -5.0 / 0.0: 3, 0.0: 4}.map(k, string(k))]" is evaluated 100 times
    Then every result is [["-2.5", "1.5", "NaN"], ["-Inf", "0", "1.5", "NaN"]]

  @REQ-EXPR-021 @id:EXPR-021-05
  Scenario Outline: Map equality and lookup compare numeric keys by value
    Given an expression environment declaring no variables
    When the expression "<expression>" is evaluated
    Then the result is <result>

    Examples:
      | expression                               | result |
      | {1: 'a', 2.0: 'b'} == {1u: 'a', 2: 'b'}  | true   |
      | {1: 'a', 2.0: 'b'} == {1.0: 'a', 2: 'b'} | true   |
      | {1: 'a', 2.0: 'b'}[1u]                   | "a"    |
      | {1.0: 'a', 2: 'b'}[1]                    | "a"    |
      | {1: 'a', 2.0: 'b'}[1.0]                  | "a"    |
      | {1u: 'a', 2.0: 'b'}[2.0]                 | "b"    |
      | {1: 'a', 'x': 0} == {'1': 'a', 'x': 0}   | false  |

  @REQ-EXPR-022 @id:EXPR-022-01
  Scenario Outline: A map with a key of no order fails when it is iterated, keeping every entry
    Given an expression environment declaring no variables
    When the expression "<expression>" is evaluated
    Then evaluation fails with code "invalid_argument"
    And the error names the expression and the cause "unordered key"

    Examples:
      | expression                                            |
      | {1: 'a', duration('1s'): 'b'}.exists(k, k == 1)        |
      | {duration('1s'): 'a'}.map(k, k)                       |
      | {timestamp('2026-11-16T09:00:00Z'): 1}.map(k, k)      |
      | {[1, 2]: 'a'}.all(k, true)                            |

  @REQ-EXPR-022 @id:EXPR-022-02
  Scenario: A map with a key of no order succeeds when its keys are not iterated
    Given an expression environment declaring no variables
    When the expression "{1: 'a', duration('1s'): 'b'}.size()" is evaluated
    Then the result is 2

  @REQ-EXPR-007 @id:EXPR-007-01
  Scenario: An evaluation over its cost limit stops with an error
    Given an expression environment declaring "items" with schema {"type": "array", "items": {"type": "integer"}} and a cost limit of 100
    When the expression "items.all(i, i > 0)" is evaluated with "items" bound to the integers 1 to 1000
    Then evaluation fails with code "invalid_argument"
    And the error names the expression and the cost limit 100

  @REQ-EXPR-007 @id:EXPR-007-02
  Scenario: The same evaluation within a higher cost limit succeeds
    Given an expression environment declaring "items" with schema {"type": "array", "items": {"type": "integer"}} and a cost limit of 1000000
    When the expression "items.all(i, i > 0)" is evaluated with "items" bound to the integers 1 to 1000
    Then the result is true

  @REQ-EXPR-007 @id:EXPR-007-03
  Scenario: The default cost limit stops a quadratic comprehension
    Given an expression environment declaring "items" with schema {"type": "array", "items": {"type": "integer"}}
    When the expression "items.all(x, items.all(y, x + y > 0))" is evaluated with "items" bound to the integers 1 to 1000
    Then evaluation fails with code "invalid_argument"
    And the error names the expression and the cost limit 1000000

  @REQ-EXPR-008 @id:EXPR-008-01
  Scenario Outline: An evaluation error names its cause
    Given an expression environment declaring "n" as an integer, "items" as a list of integers and "m" as a map of integers
    And "n" is bound to 1, "items" to [1, 2] and "m" to {"a": 1}
    When the expression "<expression>" is evaluated
    Then evaluation fails with code "invalid_argument"
    And the error names the expression and the cause "<cause>"

    Examples:
      | expression              | cause            |
      | n / 0                   | division by zero |
      | n + 9223372036854775807 | overflow         |
      | items[5]                | out of bounds    |
      | m['b']                  | no such key      |

  @REQ-EXPR-008 @REQ-EXPR-009 @id:EXPR-008-02
  Scenario: Reading an absent optional property is an evaluation error
    Given an expression environment declaring "state" with an optional string property "reviewer"
    When the expression "state.reviewer == 'ana'" is evaluated with "state" bound to {}
    Then evaluation fails with code "invalid_argument"
    And the error names the expression and the cause "no such key"

  @REQ-EXPR-008 @REQ-EXPR-012 @id:EXPR-008-03
  Scenario Outline: A result with no JSON form is an evaluation error
    Given an expression environment declaring no variables
    When the expression "<expression>" is evaluated
    Then evaluation fails with code "invalid_argument"
    And the error names the expression and the cause "no JSON form"

    Examples:
      | expression |
      | {1: 'a'}   |
      | 1.0 / 0.0  |
      | 0.0 / 0.0  |
      | int        |

  @REQ-EXPR-009 @id:EXPR-009-01
  Scenario: Testing an absent optional property with has() gives false
    Given an expression environment declaring "state" with an optional string property "reviewer"
    When the expression "has(state.reviewer) ? state.reviewer : 'nobody'" is evaluated with "state" bound to {}
    Then the result is "nobody"

  @REQ-EXPR-010 @id:EXPR-010-01
  Scenario Outline: Integers bound to a variable keep their exact value
    Given an expression environment declaring "n" with schema {"type": "integer"}
    When the expression "n + 1" is evaluated with "n" bound to <value>
    Then the result is <result>

    Examples:
      | value                | result               |
      | 9007199254740993     | 9007199254740994     |
      | -9223372036854775807 | -9223372036854775806 |
      | 3.0                  | 4                    |

  @REQ-EXPR-011 @id:EXPR-011-01
  Scenario Outline: A value that does not fit its variable is rejected
    Given an expression environment declaring "v" with schema <schema>
    When the expression "v == v" is evaluated with "v" bound to <value>
    Then evaluation fails with code "invalid_argument"
    And the error names the variable "v"

    Examples:
      | schema                                          | value               |
      | {"type": "integer"}                             | "3"                 |
      | {"type": "integer"}                             | 3.5                 |
      | {"type": "integer"}                             | 9223372036854775808 |
      | {"type": "boolean"}                             | "true"              |
      | {"type": "string", "format": "date-time"}       | "yesterday"         |
      | {"type": "array", "items": {"type": "integer"}} | [1, "2"]            |

  @REQ-EXPR-012 @id:EXPR-012-01
  Scenario Outline: A result is returned as a JSON value
    Given an expression environment declaring no variables
    When the expression "<expression>" is evaluated
    Then the result is <json>

    Examples:
      | expression                             | json                   |
      | 'a' + 'b'                              | "ab"                   |
      | [1, 2] + [3]                           | [1, 2, 3]              |
      | {'b': 1, 'a': true}                    | {"a": true, "b": 1}    |
      | uint(7)                                | 7                      |
      | 2.5 * 2.0                              | 5                      |
      | null                                   | null                   |
      | timestamp('2026-11-16T10:00:00+01:00') | "2026-11-16T09:00:00Z" |
      | duration('90s')                        | "90s"                  |
      | b'hi'                                  | "aGk="                 |

  @REQ-EXPR-012 @id:EXPR-012-02
  Scenario: An object inside an object is returned as nested objects
    Given an expression environment declaring "state" with schema {"type": "object", "properties": {"outer": {"type": "object", "properties": {"inner": {"type": "object", "properties": {"n": {"type": "integer"}, "tag": {"type": "string"}}}}}}}
    When the expression "state.outer" is evaluated with "state" bound to {"outer": {"inner": {"n": 7, "tag": "a"}}}
    Then the result is {"inner": {"n": 7, "tag": "a"}}

  @REQ-EXPR-012 @id:EXPR-012-03
  Scenario: An object inside a list is returned as objects in array order
    Given an expression environment declaring "state" with schema {"type": "object", "properties": {"items": {"type": "array", "items": {"type": "object", "properties": {"x": {"type": "integer"}}}}}}
    When the expression "state.items" is evaluated with "state" bound to {"items": [{"x": 2}, {"x": 1}]}
    Then the result is [{"x": 2}, {"x": 1}]

  @REQ-EXPR-012 @id:EXPR-012-04
  Scenario: An object inside a map is returned as an object of objects
    Given an expression environment declaring "state" with schema {"type": "object", "properties": {"byid": {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}}}
    When the expression "state.byid" is evaluated with "state" bound to {"byid": {"b": {"v": 2}, "a": {"v": 1}}}
    Then the result is {"a": {"v": 1}, "b": {"v": 2}}
