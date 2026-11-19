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

  @REQ-SCHEMA-038 @id:SCHEMA-038-01
  Scenario Outline: A byte slice is a nullable base64 string
    Given a named Go type "NamedBytes" with underlying type "[]byte"
    And a Go struct type "Payload" with fields:
      | field | type   | json tag |
      | Data  | <type> | data     |
    When a schema is generated from "Payload"
    Then property "data" of the schema has contentEncoding "base64"
    And the schema accepts the instance {"data": <accepted>}
    And the schema rejects the instance {"data": <rejected>}

    Examples:
      | type        | accepted    | rejected   |
      | []byte      | "aGk="      | 1          |
      | []byte      | "not-b64!"  | true       |
      | []byte      | null        | [104, 105] |
      | NamedBytes  | null        | 1          |
      | NamedBytes  | "aGk="      | {"a": 1}   |

  @REQ-SCHEMA-039 @id:SCHEMA-039-01
  Scenario Outline: Raw JSON admits any value
    Given a Go struct type "Blob" with fields:
      | field | type            | json tag |
      | Doc   | json.RawMessage | doc      |
    When a schema is generated from "Blob"
    Then the schema accepts the instance {"doc": <value>}

    Examples:
      | value          |
      | null           |
      | {"a": [1, 2]}  |
      | "text"         |
      | 3              |
      | [true, false]  |

  @REQ-SCHEMA-040 @id:SCHEMA-040-01
  Scenario Outline: A fixed-length byte array is an array of integers
    Given a Go struct type "Digest" with fields:
      | field | type    | json tag |
      | Sum   | [3]byte | sum      |
    When a schema is generated from "Digest"
    Then the schema accepts the instance {"sum": <accepted>}
    And the schema rejects the instance {"sum": <rejected>} with a violation at "<location>" of keyword "<keyword>"

    Examples:
      | accepted      | rejected     | location | keyword  |
      | [1, 2, 3]     | "abc"        | /sum     | type     |
      | [0, 255, 128] | null         | /sum     | type     |
      | [1, 2, 3]     | [1, 2]       | /sum     | minItems |
      | [1, 2, 3]     | [1, 2, 3, 4] | /sum     | maxItems |
      | [1, 2, 3]     | [1, "2", 3]  | /sum/1   | type     |

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

  @REQ-SCHEMA-003 @id:SCHEMA-003-02
  Scenario Outline: A composite or pointer field is required only by its tag
    Given a Go struct type "Doc" with fields:
      | field | type      | json tag |
      | V     | <go type> | <tag>    |
    When a schema is generated from "Doc"
    Then the schema <verdict> the instance {}

    Examples:
      | go type        | tag         | verdict |
      | []string       | v           | rejects |
      | map[string]int | v           | rejects |
      | []byte         | v           | rejects |
      | *int           | v           | rejects |
      | []string       | v,omitempty | accepts |
      | map[string]int | v,omitempty | accepts |
      | []byte         | v,omitempty | accepts |
      | *int           | v,omitempty | accepts |

  @REQ-SCHEMA-038 @REQ-SCHEMA-008 @id:SCHEMA-038-02
  Scenario Outline: A byte slice with its own encoding is not a base64 string
    Given a Go type "Binary"
    And the type "Binary" appears as <position> in "Doc"
    And a value of "Doc" that is the zero value
    When the value is encoded with encoding/json
    Then the schema generated from "Doc" accepts the encoded value
    And the schema rejects the instance <wrong> with a violation at "<location>" of keyword "type"

    Examples:
      | position          | wrong       | location |
      | a field named "v" | {"v": null} | /v       |
      | an embedded field | null        |          |
      | the whole type    | null        |          |

  @REQ-SCHEMA-040 @REQ-SCHEMA-008 @id:SCHEMA-040-02
  Scenario Outline: A byte array with its own encoding is not an integer array
    Given a Go type "UUID"
    And the type "UUID" appears as <position> in "Doc"
    And a value of "Doc" that is the zero value
    When a schema is generated from "Doc"
    And the value is encoded with encoding/json
    Then the schema generated from "Doc" accepts the encoded value
    And the schema rejects the instance <wrong> with a violation at "<location>" of keyword "type"

    Examples:
      | position          | wrong      | location |
      | a field named "v" | {"v": [1]} | /v       |
      | an embedded field | [1]        |          |
      | the whole type    | [1]        |          |

  @REQ-SCHEMA-041 @REQ-SCHEMA-008 @id:SCHEMA-041-01
  Scenario Outline: A value with its own text encoding is a string wherever it appears
    Given a Go type "TextStamp"
    And the type "TextStamp" appears as <position> in "Doc"
    And a value of "Doc" that is the zero value
    When a schema is generated from "Doc"
    And the value is encoded with encoding/json
    Then the schema generated from "Doc" accepts the encoded value
    And the schema rejects the instance <wrong> with a violation at "<location>" of keyword "type"

    Examples:
      | position          | wrong     | location |
      | a field named "v" | {"v": {}} | /v       |
      | an embedded field | {}        |          |
      | the whole type    | {}        |          |

  @REQ-SCHEMA-042 @REQ-SCHEMA-008 @id:SCHEMA-042-01
  Scenario Outline: A value with its own JSON encoding admits every value
    Given a Go type "JSONStamp"
    And the type "JSONStamp" appears as <position> in "Doc"
    And a value of "Doc" that is the zero value
    When a schema is generated from "Doc"
    And the value is encoded with encoding/json
    Then the schema generated from "Doc" accepts the encoded value
    And the schema accepts the instance <other>

    Examples:
      | position          | other         |
      | a field named "v" | {"v": [1, 2]} |
      | an embedded field | [1, 2]        |
      | the whole type    | [1, 2]        |

  @REQ-SCHEMA-043 @REQ-SCHEMA-044 @REQ-SCHEMA-008 @id:SCHEMA-043-01
  Scenario Outline: A type handled in its own right keeps the schema of its encoding
    Given a Go type "<go type>"
    And the type "<go type>" appears as <position> in "Doc"
    And a value of "Doc" that is the zero value
    When a schema is generated from "Doc"
    And the value is encoded with encoding/json
    Then the schema generated from "Doc" accepts the encoded value
    And the schema rejects the instance <wrong> with a violation at "<location>" of keyword "type"

    Examples:
      | go type   | position          | wrong      | location |
      | TimeStamp | a field named "v" | {"v": 1}   | /v       |
      | TimeStamp | the whole type    | 1          |          |
      | Numbered  | a field named "v" | {"v": "1"} | /v       |
      | Numbered  | an embedded field | "1"        |          |
      | Numbered  | the whole type    | "1"        |          |

  @REQ-SCHEMA-045 @REQ-SCHEMA-008 @id:SCHEMA-045-01
  Scenario: A value whose encoding only its pointer has admits both encodings
    Given a Go type "PointerStamp"
    And a Go struct type "Doc" with fields:
      | field | type         | json tag |
      | V     | PointerStamp | v        |
    And a value of "Doc" that is the zero value
    When the address of the value is encoded with encoding/json
    Then the schema generated from "Doc" accepts the encoded value
    And the schema accepts the instance {"v": [1]}

  @REQ-SCHEMA-046 @id:SCHEMA-046-01
  Scenario Outline: A string-option number or boolean is a string
    Given a Go struct type "Inner" with fields:
      | field | type      | json tag |
      | V     | <go type> | v,string |
    And the type "Inner" appears as <position> in "Doc"
    And a value of "Doc" that is the zero value
    When a schema is generated from "Doc"
    And the value is encoded with encoding/json
    Then the schema generated from "Doc" accepts the encoded value
    And the schema rejects the instance {"v": <literal>} with a violation at "/v" of keyword "type"

    Examples:
      | go type | literal | position          |
      | int     | 0       | the whole type    |
      | int     | 0       | an embedded field |
      | int64   | 0       | the whole type    |
      | int64   | 0       | an embedded field |
      | uint    | 0       | the whole type    |
      | uint    | 0       | an embedded field |
      | float64 | 0       | the whole type    |
      | float64 | 0       | an embedded field |
      | bool    | false   | the whole type    |
      | bool    | false   | an embedded field |

  @REQ-SCHEMA-047 @id:SCHEMA-047-01
  Scenario Outline: A string-option string holds the quoted string
    Given a Go struct type "Inner" with fields:
      | field | type   | json tag |
      | V     | string | v,string |
    And the type "Inner" appears as <position> in "Doc"
    And a value of "Doc" that is the zero value
    When a schema is generated from "Doc"
    And the value is encoded with encoding/json
    Then the schema generated from "Doc" accepts the encoded value
    And the schema rejects the instance {"v": "plain"} with a violation at "/v" of keyword "pattern"

    Examples:
      | position          |
      | the whole type    |
      | an embedded field |

  @REQ-SCHEMA-048 @id:SCHEMA-048-01
  Scenario Outline: A map whose key type is written as a string is an object
    Given a Go type "<map type>"
    And the type "<map type>" appears as <position> in "Doc"
    When a schema is generated from "Doc"
    Then the schema accepts the instance <accepted>
    And the schema rejects the instance <wrong> with a violation at "<location>" of keyword "type"

    Examples:
      | map type  | position          | accepted           | wrong            | location |
      | IntKeyed  | a field named "v" | {"v": {"7": "x"}}  | {"v": {"7": 1}}  | /v/7     |
      | IntKeyed  | an embedded field | {"v": {"7": "x"}}  | {"v": {"7": 1}}  | /v/7     |
      | IntKeyed  | the whole type    | {"7": "x"}         | {"7": 1}         | /7       |
      | TextKeyed | a field named "v" | {"v": {"S0": "x"}} | {"v": {"S0": 1}} | /v/S0    |
      | TextKeyed | an embedded field | {"v": {"S0": "x"}} | {"v": {"S0": 1}} | /v/S0    |
      | TextKeyed | the whole type    | {"S0": "x"}        | {"S0": 1}        | /S0      |

  @REQ-SCHEMA-049 @id:SCHEMA-049-01
  Scenario: A map key type with no JSON text is rejected with its name
    Given a Go type "FloatKeyed"
    And a Go struct type "Doc" with fields:
      | field | type       | json tag |
      | M     | FloatKeyed | m        |
    When a schema is generated from "Doc"
    Then the generation fails with code "invalid_argument" naming the field path "M" and the key type "float64"
