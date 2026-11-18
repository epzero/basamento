@planned @wp:WP-L2-02 @level:component
Feature: Canonical JSON
  A flow is identified by the hash of its canonical JSON, and recorded runs
  are compared byte by byte. Two texts with the same value must therefore give
  the same bytes, and a text whose value would change on the way must be
  refused rather than silently rounded.

  @REQ-SCHEMA-030 @id:SCHEMA-030-01
  Scenario: Whitespace is removed and members are sorted, arrays keep their order
    Given the JSON text:
      """
      {
        "version": 3,
        "flow": "codegen.feature",
        "edges": [ {"to": "plan", "from": "START"}, {"to": "END", "from": "plan"} ]
      }
      """
    When the JSON text is canonicalized
    Then the canonical form is:
      """
      {"edges":[{"from":"START","to":"plan"},{"from":"plan","to":"END"}],"flow":"codegen.feature","version":3}
      """

  @REQ-SCHEMA-030 @id:SCHEMA-030-02
  Scenario: Member names are ordered by UTF-16 code units, not by code points
    Given the JSON text:
      """
      {"｡": 1, "😀": 2, "b": 3, "B": 4, "": 5}
      """
    When the JSON text is canonicalized
    Then the member names of the canonical form are in the order "", "B", "b", "😀", "｡"

  @REQ-SCHEMA-031 @id:SCHEMA-031-01
  Scenario Outline: Numbers are written in their shortest ECMAScript form
    Given the JSON text <text>
    When the JSON text is canonicalized
    Then the canonical form is <canonical>

    Examples:
      | text                 | canonical             |
      | 1.0                  | 1                     |
      | 1e2                  | 100                   |
      | 100e-2               | 1                     |
      | -0                   | 0                     |
      | -1.50                | -1.5                  |
      | 0.000001             | 0.000001              |
      | 1E-7                 | 1e-7                  |
      | 1e21                 | 1e+21                 |
      | 0.1                  | 0.1                   |
      | 9007199254740992     | 9007199254740992      |

  @REQ-SCHEMA-032 @id:SCHEMA-032-01
  Scenario: Strings are written in UTF-8 with only the necessary escapes
    Given the JSON text:
      """
      ["A", "\/", "é", "€", "a\u0009b", "\u001f", "\"", "\\"]
      """
    When the JSON text is canonicalized
    Then the canonical form is:
      """
      ["A","/","é","€","a\tb","\u001f","\"","\\"]
      """

  @REQ-SCHEMA-033 @id:SCHEMA-033-01
  Scenario: Texts with the same value give the same bytes
    Given the JSON text "first":
      """
      {"a": [1.0, "é", true], "b": {"y": null, "x": 1e2}}
      """
    And the JSON text "second":
      """
      { "b" : { "x" : 100, "y" : null }, "a" : [ 1, "é", true ] }
      """
    When both JSON texts are canonicalized
    Then the canonical forms of "first" and "second" are identical

  @REQ-SCHEMA-034 @id:SCHEMA-034-01
  Scenario: Canonicalizing a canonical form changes nothing
    Given the JSON text:
      """
      {"n": [0.1, 1e+21, -1.5], "s": "é\u001f", "z": {"😀": false}}
      """
    When the JSON text is canonicalized
    And its canonical form is canonicalized again
    Then the second canonical form is identical to the first

  @REQ-SCHEMA-035 @id:SCHEMA-035-01
  Scenario Outline: A number that would change value is refused at its location
    Given the JSON text {"run": {"n": <number>}}
    When the JSON text is canonicalized
    Then the canonicalization fails with code "invalid_argument" at instance location "/run/n"

    Examples:
      | number                  |
      | 9007199254740993        |
      | 12345678901234567890    |
      | 0.10000000000000000001  |
      | 1e400                   |
      | 1e-400                  |

  @REQ-SCHEMA-036 @id:SCHEMA-036-01
  Scenario: A duplicated member name is refused at the second member
    Given the JSON text:
      """
      {"nodes": {"plan": {"kind": "agent"}, "plan": {"kind": "tool"}}}
      """
    When the JSON text is canonicalized
    Then the canonicalization fails with code "invalid_argument" at instance location "/nodes/plan"

  @REQ-SCHEMA-037 @id:SCHEMA-037-01
  Scenario Outline: An unpaired surrogate is refused at its string
    Given the JSON text {"names": ["ok", "<string>"]}
    When the JSON text is canonicalized
    Then the canonicalization fails with code "invalid_argument" at instance location "/names/1"

    Examples:
      | string        |
      | \ud800        |
      | \udc00        |
      | a\ud83d       |
