@wp:WP-L2-01 @level:component
Feature: Variable types from JSON Schema
  Each declared variable gets the CEL type its JSON Schema maps to
  (specs/expr/design.md §2), so expressions are checked against the shape of
  the state, the parameters or the principal they read.

  @REQ-EXPR-003 @id:EXPR-003-01
  Scenario Outline: A schema gives its variable a CEL type
    Given an expression environment declaring "v" with schema <schema>
    When the expression "<expression>" is compiled
    Then compilation succeeds with result type "<type>"

    Examples:
      | schema                                                         | expression | type                      |
      | {"type": "boolean"}                                            | v          | bool                      |
      | {"type": "integer"}                                            | v          | int                       |
      | {"type": "number"}                                             | v          | double                    |
      | {"type": "string"}                                             | v          | string                    |
      | {"type": "string", "format": "date-time"}                      | v          | google.protobuf.Timestamp |
      | {"type": "null"}                                               | v          | null_type                 |
      | {"type": "array", "items": {"type": "integer"}}                | v          | list(int)                 |
      | {"type": "array"}                                              | v          | list(dyn)                 |
      | {"type": "object", "properties": {"a": {"type": "integer"}}}   | v.a        | int                       |
      | {"type": "object", "additionalProperties": {"type": "string"}} | v          | map(string, string)       |
      | {"type": "object"}                                             | v          | map(string, dyn)          |
      | {"enum": ["low", "high"]}                                      | v          | string                    |
      | {"type": ["string", "null"]}                                   | v          | dyn                       |
      | {"anyOf": [{"type": "integer"}, {"type": "string"}]}           | v          | dyn                       |
      | {}                                                             | v          | dyn                       |
      | {"$defs": {"n": {"type": "integer"}}, "$ref": "#/$defs/n"}     | v          | int                       |

  @REQ-EXPR-003 @id:EXPR-003-02
  Scenario: A schema that refers to itself is typed dyn where it recurs
    Given an expression environment declaring "v" with schema:
      """json
      {
        "$defs": {
          "node": {
            "type": "object",
            "properties": {
              "name":     { "type": "string" },
              "children": { "type": "array", "items": { "$ref": "#/$defs/node" } }
            }
          }
        },
        "$ref": "#/$defs/node"
      }
      """
    When the expression "v.children" is compiled
    Then compilation succeeds with result type "list(dyn)"

  @REQ-EXPR-003 @id:EXPR-003-03
  Scenario: A schema that refers to another document is rejected
    Given an expression environment declaring "v" with schema {"$ref": "https://schemas.example.test/state.json"}
    When the expression "v" is compiled
    Then compilation fails with code "invalid_argument"
    And the error names the reference "https://schemas.example.test/state.json"

  @REQ-EXPR-003 @id:EXPR-003-04
  Scenario Outline: A schema that cannot be given a type is rejected
    Given an expression environment declaring "v" with schema <schema>
    When the expression "v" is compiled
    Then compilation fails with code "invalid_argument"

    Examples:
      | schema                      |
      | false                       |
      | {"$ref": "#/$defs/missing"} |

  @REQ-EXPR-003 @id:EXPR-003-05
  Scenario Outline: An object reached through additionalProperties keeps its field access
    Given an expression environment declaring "x" with schema <schema>
    When the expression "x['k'].v" is compiled
    Then compilation succeeds with result type "int"

    Examples:
      | schema |
      | {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}} |
      | {"$defs": {"item": {"type": "object", "properties": {"v": {"type": "integer"}}}}, "type": "object", "additionalProperties": {"$ref": "#/$defs/item"}} |
      | {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}, "additionalProperties": {"type": "string"}}} |

  @REQ-EXPR-003 @id:EXPR-003-06
  Scenario: A field of an object reached through additionalProperties is read when evaluated
    Given an expression environment declaring "x" with schema {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}
    When the expression "x['k'].v + 1" is evaluated with "x" bound to {"k": {"v": 41}}
    Then the result is 42

  @REQ-EXPR-003 @id:EXPR-003-07
  Scenario: A guard over an object reached through additionalProperties compiles to a boolean
    Given an expression environment declaring "x" with schema {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}
    When the expression "x['k'].v > 0" is compiled expecting a result of schema {"type": "boolean"}
    Then compilation succeeds with result type "bool"
