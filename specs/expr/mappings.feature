@wp:WP-L2-01 @level:component
Feature: JSON mappings
  A mapping reshapes one JSON value into another with gojq, for example a
  provider's request and response mappings. Like an expression, it reads no
  clock, environment or further input, iterates objects in key order and is
  bounded by a deterministic step limit.

  @REQ-EXPR-013 @id:EXPR-013-01
  Scenario: A mapping returns the one value it produces
    Given the mapping "{prompt: .text, n: ($params.count // 1)}" with variable "$params"
    When the mapping is evaluated on {"text": "a red kite"} with "$params" bound to {"count": 2}
    Then the result is {"prompt": "a red kite", "n": 2}

  @REQ-EXPR-014 @id:EXPR-014-01
  Scenario Outline: A mapping that produces no value or several values fails
    Given the mapping "<mapping>"
    When the mapping is evaluated on <input>
    Then evaluation fails with code "invalid_argument"
    And the error states that the mapping produced <count>

    Examples:
      | mapping   | input         | count               |
      | empty     | {}            | no value            |
      | .items[]  | {"items": []} | no value            |
      | .[]       | [1, 2, 3]     | more than one value |
      | repeat(1) | null          | more than one value |

  @REQ-EXPR-015 @id:EXPR-015-01
  Scenario Outline: A mapping that reads the clock, the environment or other inputs is rejected
    When the mapping "<mapping>" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names "<name>"

    Examples:
      | mapping                  | name           |
      | now                      | now            |
      | now \| todate            | now            |
      | 0 \| localtime           | localtime      |
      | 0 \| strflocaltime("%H") | strflocaltime  |
      | env.HOME                 | env            |
      | $ENV.HOME                | $ENV           |
      | input                    | input          |
      | [inputs]                 | inputs         |
      | input_filename           | input_filename |
      | debug                    | debug          |
      | stderr                   | stderr         |
      | halt                     | halt           |
      | halt_error               | halt_error     |
      | import "lib" as lib; .   | import         |
      | include "lib"; .         | include        |
      | "lib" \| modulemeta      | modulemeta     |
      | def f: now; 1            | now            |

  @REQ-EXPR-015 @id:EXPR-015-02
  Scenario Outline: Time functions on given values are allowed
    Given the mapping "<mapping>"
    When the mapping is evaluated on <input>
    Then the result is <result>

    Examples:
      | mapping  | input                  | result                 |
      | todate   | 0                      | "1970-01-01T00:00:00Z" |
      | fromdate | "2026-11-16T09:00:00Z" | 1794819600             |

  @REQ-EXPR-016 @id:EXPR-016-01
  Scenario Outline: A mapping iterates objects in ascending key order
    Given the mapping "<mapping>"
    When the mapping is evaluated 100 times on {"c": 3, "a": 1, "b": 2}
    Then every result is <result>

    Examples:
      | mapping                 | result                      |
      | [.[]]                   | [1, 2, 3]                   |
      | keys                    | ["a", "b", "c"]             |
      | to_entries \| map(.key) | ["a", "b", "c"]             |
      | tojson                  | "{\"a\":1,\"b\":2,\"c\":3}" |

  @REQ-EXPR-017 @id:EXPR-017-01
  Scenario Outline: A mapping over its step limit stops with an error
    Given the mapping "<mapping>" with a step limit of 100000
    When the mapping is evaluated on null
    Then evaluation fails with code "invalid_argument"
    And the error names the step limit 100000

    Examples:
      | mapping                 |
      | last(range(1000000000)) |
      | [repeat(1)]             |
      | def f: f; f             |
      | until(false; .)         |

  @REQ-EXPR-017 @id:EXPR-017-02
  Scenario: A mapping within its step limit succeeds
    Given the mapping "[range(10)] | add" with a step limit of 100000
    When the mapping is evaluated on null
    Then the result is 45

  @REQ-EXPR-017 @id:EXPR-017-03
  Scenario: The default step limit stops a long mapping
    Given the mapping "last(range(1000000000))"
    When the mapping is evaluated on null
    Then evaluation fails with code "invalid_argument"
    And the error names the step limit 1000000

  @REQ-EXPR-018 @id:EXPR-018-01
  Scenario Outline: Large integers pass through a mapping exactly
    Given the mapping "<mapping>"
    When the mapping is evaluated on {"id": 12345678901234567890}
    Then the result is <result>

    Examples:
      | mapping   | result                       |
      | .id       | 12345678901234567890         |
      | {id: .id} | {"id": 12345678901234567890} |
      | [.id]     | [12345678901234567890]       |

  @REQ-EXPR-019 @id:EXPR-019-01
  Scenario: A mapping that does not parse is rejected
    When the mapping "{prompt: .text" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names the mapping and the position of the error
