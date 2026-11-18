@wp:WP-L2-02 @level:component
Feature: Generating schemas from Go types
  Model parameters, structured outputs and gate payloads are often declared as
  Go types. The schema generated from a type is what flows store and hash, so
  it must follow the type exactly and come out the same every time.

  @REQ-SCHEMA-001 @REQ-SCHEMA-003 @id:SCHEMA-001-01
  Scenario: Properties are named as encoding/json names the fields
    Given a Go struct type "Review" with fields:
      | field    | type   | json tag           |
      | Approved | bool   | approved           |
      | Notes    | string | notes,omitempty    |
      | Score    | int    |                    |
      | Internal | string | -                  |
    When a schema is generated from "Review"
    Then the schema has exactly the properties "Score", "approved" and "notes"
    And the schema requires exactly the properties "Score" and "approved"

  @REQ-SCHEMA-002 @id:SCHEMA-002-01
  Scenario Outline: A property accepts the JSON values of its Go type
    Given a Go struct type "Holder" with fields:
      | field | type      | json tag |
      | X     | <go type> | x        |
    When a schema is generated from "Holder"
    Then the schema accepts the instance {"x": <accepted>}
    And the schema rejects the instance {"x": <rejected>} with a violation at "<location>" of keyword "type"

    Examples:
      | go type        | accepted          | rejected          | location |
      | string         | "a"               | 1                 | /x       |
      | int            | 3                 | 3.5               | /x       |
      | int64          | -4                | "4"               | /x       |
      | float64        | 3.5               | "3.5"             | /x       |
      | bool           | true              | "true"            | /x       |
      | []string       | ["a", "b"]        | ["a", 1]          | /x/1     |
      | []string       | ["a", "b"]        | ["a", null]       | /x/1     |
      | []string       | null              | ["a", 1]          | /x/1     |
      | [2]int         | [1, 2]            | [1, "2"]          | /x/1     |
      | [2]int         | [1, 2]            | null              | /x       |
      | map[string]int | {"a": 1}          | {"a": "1"}        | /x/a     |
      | map[string]int | {"a": 1}          | {"a": null}       | /x/a     |
      | map[string]int | null              | {"a": "1"}        | /x/a     |

  @REQ-SCHEMA-002 @id:SCHEMA-002-02
  Scenario: A nested struct becomes a nested object with its own rules
    Given a Go struct type "Reviewer" with fields:
      | field | type   | json tag |
      | Login | string | login    |
    And a Go struct type "Gate" with fields:
      | field     | type       | json tag  |
      | Reviewers | []Reviewer | reviewers |
    When a schema is generated from "Gate"
    Then the schema accepts the instance {"reviewers": [{"login": "ana"}]}
    And the schema rejects the instance {"reviewers": [{"login": 7}]} with a violation at "/reviewers/0/login" of keyword "type"

  @REQ-SCHEMA-003 @id:SCHEMA-003-01
  Scenario Outline: Only fields without omitempty or omitzero are required
    Given a Go struct type "Options" with fields:
      | field | type   | json tag |
      | Mode  | string | <tag>    |
    When a schema is generated from "Options"
    Then the schema <verdict> the instance {}

    Examples:
      | tag            | verdict  |
      | mode           | rejects  |
      | mode,omitempty | accepts  |
      | mode,omitzero  | accepts  |

  @REQ-SCHEMA-004 @id:SCHEMA-004-01
  Scenario: A property the struct does not have is not allowed
    Given a Go struct type "Review" with fields:
      | field    | type | json tag |
      | Approved | bool | approved |
    When a schema is generated from "Review"
    Then the schema rejects the instance {"approved": true, "extra": 1} with a violation at "/extra" of keyword "additionalProperties"

  @REQ-SCHEMA-005 @id:SCHEMA-005-01
  Scenario: A jsonschema tag becomes the property's description
    Given a Go struct type "Review" with fields:
      | field    | type | json tag | jsonschema tag                 |
      | Approved | bool | approved | whether the change is accepted |
    When a schema is generated from "Review"
    Then property "approved" of the schema has the description "whether the change is accepted"

  @REQ-SCHEMA-006 @id:SCHEMA-006-01
  Scenario Outline: A field without a JSON form is rejected with its path
    Given a Go struct type "Inner" with fields:
      | field | type      | json tag |
      | Bad   | <go type> | bad      |
    And a Go struct type "Outer" with fields:
      | field | type  | json tag |
      | In    | Inner | in       |
    When a schema is generated from "Outer"
    Then the generation fails with code "invalid_argument" naming the field path "In.Bad"

    Examples:
      | go type    |
      | chan int   |
      | func()     |
      | complex128 |

  @REQ-SCHEMA-007 @id:SCHEMA-007-01
  Scenario: Generating twice gives the same bytes
    Given a Go struct type "Params" with fields:
      | field  | type           | json tag         |
      | Prompt | string         | prompt           |
      | Seed   | int            | seed,omitempty   |
      | Tags   | []string       | tags             |
      | Extra  | map[string]int | extra,omitempty  |
    When a schema is generated from "Params" as "first"
    And a schema is generated from "Params" as "second"
    Then the canonical forms of schemas "first" and "second" are identical

  @REQ-SCHEMA-008 @id:SCHEMA-008-01
  Scenario Outline: An encoded value satisfies the schema of its type
    Given a Go struct type "Params" with fields:
      | field | type           | json tag        |
      | Name  | string         | name            |
      | Tags  | []string       | tags            |
      | Extra | map[string]int | extra           |
      | Seed  | int            | seed,omitempty  |
    And a value of "Params" that is <value>
    When the value is encoded with encoding/json
    Then the schema generated from "Params" accepts the encoded value

    Examples:
      | value                                          |
      | the zero value                                 |
      | name "a", tags "x" and "y", extra a=1, seed 9  |
