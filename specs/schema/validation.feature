@planned @wp:WP-L2-02 @level:component
Feature: Compiling schemas and validating instances
  Gate payloads, model parameters and structured outputs are checked against
  schemas that people and LLMs write. A rejected instance must say exactly
  where it is wrong, in the same words every time, and compiling a schema must
  never reach outside the schema set.

  @REQ-SCHEMA-010 @id:SCHEMA-010-01
  Scenario: A schema without $schema is read as draft 2020-12
    Given the schema:
      """
      {"type": "array", "prefixItems": [{"type": "string"}, {"type": "integer"}]}
      """
    When the schema is compiled
    Then the schema accepts the instance ["a", 1]
    And the schema rejects the instance ["a", "b"] with a violation at "/1" of keyword "type"

  @REQ-SCHEMA-011 @id:SCHEMA-011-01
  Scenario Outline: An invalid schema is rejected at its location
    Given the schema:
      """
      <schema>
      """
    When the schema is compiled
    Then the compilation fails with code "invalid_argument" at schema location "<location>"

    Examples:
      | schema                                                  | location             |
      | {"type": "strnig"}                                      | /type                |
      | {"type": "object", "required": "approved"}              | /required            |
      | {"properties": {"n": {"minimum": "3"}}}                 | /properties/n/minimum |

  @REQ-SCHEMA-012 @security @id:SCHEMA-012-01
  Scenario Outline: A reference outside the schema set is rejected without fetching it
    Given the schema:
      """
      {"$ref": "<reference>"}
      """
    When the schema is compiled
    Then the compilation fails with code "invalid_argument" naming the reference "<reference>"
    And no network request or file read was made

    Examples:
      | reference                            |
      | https://schemas.example.com/review.json |
      | file:///etc/passwd                   |
      | review.json                          |

  @REQ-SCHEMA-013 @id:SCHEMA-013-01
  Scenario: A reference to another schema of the set is resolved
    Given a schema set with the schema "https://basamento.dev/schemas/review.json":
      """
      {"type": "object", "properties": {"approved": {"type": "boolean"}}, "required": ["approved"]}
      """
    And the schema "https://basamento.dev/schemas/gate.json" in the same schema set:
      """
      {"type": "object", "properties": {"review": {"$ref": "review.json"}}}
      """
    When schema "https://basamento.dev/schemas/gate.json" of the schema set is compiled
    Then the schema accepts the instance {"review": {"approved": true}}
    And the schema rejects the instance {"review": {"approved": "yes"}} with a violation at "/review/approved" of keyword "type"

  @REQ-SCHEMA-020 @id:SCHEMA-020-01
  Scenario Outline: Each violation names its instance location and keyword
    Given the schema:
      """
      {
        "type": "object",
        "properties": {
          "reviewers": {"type": "array", "items": {"type": "string"}, "maxItems": 2},
          "score": {"type": "integer", "minimum": 0},
          "state": {"enum": ["open", "closed"]},
          "a/b": {"type": "string"},
          "m~n": {"type": "string"}
        }
      }
      """
    When the instance <instance> is validated
    Then the validation fails with code "invalid_argument"
    And the only violation is at "<location>" of keyword "<keyword>"

    Examples:
      | instance                               | location     | keyword  |
      | {"reviewers": ["ana", 7]}              | /reviewers/1 | type     |
      | {"reviewers": ["ana", "bo", "cy"]}     | /reviewers   | maxItems |
      | {"score": -1}                          | /score       | minimum  |
      | {"state": "merged"}                    | /state       | enum     |
      | {"a/b": 1}                             | /a~1b        | type     |
      | {"m~n": 1}                             | /m~0n        | type     |
      | []                                     |              | type     |

  @REQ-SCHEMA-021 @id:SCHEMA-021-01
  Scenario Outline: A missing or disallowed property is located at the property
    Given the schema:
      """
      {"type": "object", "properties": {"approved": {"type": "boolean"}}, "required": ["approved"], "additionalProperties": false}
      """
    When the instance <instance> is validated
    Then the only violation is at "<location>" of keyword "<keyword>"

    Examples:
      | instance                           | location  | keyword              |
      | {}                                 | /approved | required             |
      | {"approved": true, "extra": 1}     | /extra    | additionalProperties |

  @REQ-SCHEMA-022 @id:SCHEMA-022-01
  Scenario: All violations are listed in a fixed order
    Given the schema:
      """
      {"type": "object", "properties": {"b": {"type": "string", "minLength": 3}, "a": {"type": "integer"}}, "required": ["c"]}
      """
    When the instance {"b": 1, "a": "x"} is validated 3 times
    Then each validation lists exactly these violations, in this order:
      | location | keyword  |
      | /a       | type     |
      | /b       | type     |
      | /c       | required |

  @REQ-SCHEMA-023 @id:SCHEMA-023-01
  Scenario Outline: Numbers are compared by their exact value
    Given the schema:
      """
      <schema>
      """
    When the instance <instance> is validated
    Then the instance is <verdict>

    Examples:
      | schema                                   | instance          | verdict  |
      | {"maximum": 9007199254740992}            | 9007199254740993  | rejected |
      | {"maximum": 9007199254740992}            | 9007199254740992  | accepted |
      | {"type": "number", "multipleOf": 0.1}    | 0.3               | accepted |
      | {"type": "integer"}                      | 1.0               | accepted |
      | {"type": "integer"}                      | 1.5               | rejected |
      | {"exclusiveMinimum": 0.1}                | 0.10000000000000000001 | accepted |

  @REQ-SCHEMA-024 @id:SCHEMA-024-01
  Scenario Outline: Listed formats are asserted
    Given the schema:
      """
      {"type": "string", "format": "<format>"}
      """
    When the instance "<value>" is validated
    Then the instance is <verdict>

    Examples:
      | format    | value                                | verdict  |
      | date-time | 2026-11-16T09:00:00Z                 | accepted |
      | date-time | 2026-11-16 09:00                     | rejected |
      | date      | 2026-02-30                           | rejected |
      | duration  | PT72H                                | accepted |
      | email     | ana@example.com                      | accepted |
      | email     | ana.example.com                      | rejected |
      | uuid      | 0190a8e2-6c3b-7d4e-8f00-123456789abc | accepted |
      | uuid      | 0190a8e2                             | rejected |

  @REQ-SCHEMA-024 @id:SCHEMA-024-02
  Scenario: A format violation names the string's location
    Given the schema:
      """
      {"type": "object", "properties": {"due": {"type": "string", "format": "date"}}}
      """
    When the instance {"due": "tomorrow"} is validated
    Then the only violation is at "/due" of keyword "format"

  @REQ-SCHEMA-026 @id:SCHEMA-026-01
  Scenario: A format that is not listed accepts any string
    Given the schema:
      """
      {"type": "string", "format": "color"}
      """
    When the instance "not a color" is validated
    Then the instance is accepted

  @REQ-SCHEMA-025 @security @id:SCHEMA-025-01
  Scenario: An instance with a duplicated member name is rejected
    Given the schema:
      """
      {"type": "object", "properties": {"approved": {"const": true}}}
      """
    When the instance {"approved": true, "approved": false} is validated
    Then the validation fails with code "invalid_argument" at instance location "/approved"
