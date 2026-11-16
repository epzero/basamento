@planned @wp:WP-L2-01 @level:component
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
      | items[5]                | out of range     |
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
