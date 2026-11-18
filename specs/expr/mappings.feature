@planned @wp:WP-L2-01 @level:component
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

  @REQ-EXPR-020 @id:EXPR-020-01
  Scenario Outline: A mapping whose output exceeds its output limit stops before it builds that much
    Given the mapping "<mapping>" with an output limit of 1024 bytes
    When the mapping is evaluated on null
    Then evaluation fails with code "invalid_argument"
    And the error names the output limit 1024

    Examples:
      | mapping                                          |
      | "x" * 1025                                       |
      | "x" * 1023                                       |
      | "x" * 1000000000                                 |
      | ("x" * 1023) + "yy"                              |
      | reduce range(30) as $i ("x"; . + .)              |
      | [range(1032)]                                    |
      | [range(100000)]                                  |
      | [range(100001)] \| .[0]                          |

  @REQ-EXPR-020 @id:EXPR-020-02
  Scenario Outline: A mapping whose output is within its output limit succeeds
    Given the mapping "<mapping>" with an output limit of 1024 bytes
    When the mapping is evaluated on null
    Then the result is written compactly in <length> bytes

    Examples:
      | mapping                                 | length |
      | "x" * 1022                              | 1024   |
      | ("x" * 1020) + "yy"                     | 1024   |
      | [range(256)]                            | 915    |
      | {a: ("x" * 500), b: ("y" * 500)}        | 1015   |

  @REQ-EXPR-020 @id:EXPR-020-03
  Scenario: The default output limit stops a value of 2 GiB
    Given the mapping ""x" * 2147483647"
    When the mapping is evaluated on null
    Then evaluation fails with code "invalid_argument"
    And the error names the output limit 1048576

  @REQ-EXPR-025 @id:EXPR-025-01 @security
  Scenario Outline: A mapping that calls, defines or takes a reserved name as a parameter is rejected
    When the mapping "<mapping>" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names "<name>" and says the name is reserved

    Examples:
      | mapping                                  | name            |
      | def now: 1; now                          | now             |
      | def halt: 1; . \| halt                   | halt            |
      | def env: 1; . \| env                   | env             |
      | def env: 1; env                          | env             |
      | def f(env): env; f(1)                    | env             |
      | def f(env): 1; f(2) \| env               | env             |
      | def f: def g: now; g; f                  | now             |
      | {entries: (def inputs: 1; inputs)}       | inputs          |
      | def f(input): {"in": input}; f(1)        | input           |
      | def f(input_filename): input_filename; f(1) | input_filename |
      | def f(input_line_number): input_line_number; f(1) | input_line_number |
      | def f(debug): debug; f(1)                | debug           |
      | def f(stderr): stderr; f(1)              | stderr          |
      | def h: halt_error; h                     | halt_error      |
      | def f: localtime; f                      | localtime       |
      | def f: strflocaltime("%H"); f            | strflocaltime   |
      | def f: modulemeta; f                     | modulemeta      |
      | get_search_list                          | get_search_list |

  @REQ-EXPR-026 @id:EXPR-026-01 @security
  Scenario Outline: A mapping that uses a reserved variable is rejected
    When the mapping "<mapping>" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names the reserved variable "<variable>"

    Examples:
      | mapping             | variable       |
      | $ENV.HOME           | $ENV           |
      | {$ENV}              | $ENV           |
      | $__loc__.file       | $__loc__       |
      | [$__prog_args]      | $__prog_args   |
      | {$__prog_args}      | $__prog_args   |
      | def f: $__loc__; f  | $__loc__       |
      | [.a, $ENV]          | $ENV           |

  @REQ-EXPR-027 @id:EXPR-027-01
  Scenario Outline: Reserved names used as data are allowed
    Given the mapping "<mapping>"
    When the mapping is evaluated on <input>
    Then the result is <result>

    Examples:
      | mapping          | input                       | result                      |
      | .now             | {"now": "2026-11-16"}       | "2026-11-16"                |
      | .input           | {"input": 5}                | 5                           |
      | .env             | {"env": {"HOME": "/x"}}     | {"HOME": "/x"}              |
      | {now}            | {"now": 1}                  | {"now": 1}                  |
      | {"now": 1}       | null                        | {"now": 1}                  |
      | "now"            | null                        | "now"                       |
      | getpath(["env"]) | {"env": true}               | true                        |
      | has("halt")      | {"halt": 1}                 | true                        |

  @REQ-EXPR-028 @id:EXPR-028-01 @security
  Scenario Outline: A mapping that imports or includes a module is rejected
    When the mapping "<mapping>" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names "<name>"

    Examples:
      | mapping                  | name    |
      | include "lib";           | include |
      | import "lib" as lib; .   | import  |
      | import "lib" as $lib; .  | import  |
