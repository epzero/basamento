@planned @wp:WP-L3-05 @level:component
Feature: Versioned prompt templates
  A prompt template is registered under an id and a version, never changes
  afterwards, and is assembled only from the inputs it declares.

  @REQ-PROMPT-001 @id:PROMPT-001-01
  Scenario: A reference resolves to exactly the version it names
    Given the prompt registry holds template "greeting@1" with static segment "Say hello."
    And the prompt registry holds template "greeting@2" with static segment "Say hello politely."
    When "greeting@1" is resolved
    Then the resolved template has static segment "Say hello."

  @REQ-PROMPT-002 @id:PROMPT-002-01
  Scenario: Registering different content under an existing version is refused
    Given the prompt registry holds template "greeting@1" with static segment "Say hello."
    When template "greeting@1" is registered with static segment "Say goodbye."
    Then the registration fails with code "conflict"
    And "greeting@1" resolves to a template with static segment "Say hello."

  @REQ-PROMPT-002 @id:PROMPT-002-02
  Scenario: Registering identical content again is accepted and changes nothing
    Given the prompt registry holds template "greeting@1" with static segment "Say hello."
    When template "greeting@1" is registered with static segment "Say hello."
    Then the registration succeeds
    And the prompt registry holds 1 version of template "greeting"

  @REQ-PROMPT-003 @id:PROMPT-003-01
  Scenario Outline: A reference that does not pin a registered version is refused
    Given the prompt registry holds template "greeting@1" with static segment "Say hello."
    When "<reference>" is resolved
    Then the resolution fails with code "<code>"

    Examples:
      | reference  | code             |
      | greeting   | invalid_argument |
      | greeting@  | invalid_argument |
      | greeting@9 | not_found        |
      | farewell@1 | not_found        |

  @REQ-PROMPT-004 @id:PROMPT-004-01
  Scenario: A template that uses an undeclared variable is refused
    Given a template "answer@1" that declares input "question" and the segments
      | segment | kind | text                         |
      | turn    | user | {{question}} in {{language}} |
    When the template is registered
    Then the registration fails with code "invalid_argument" naming variable "language"
    And "answer@1" does not resolve

  @REQ-PROMPT-005 @id:PROMPT-005-01
  Scenario Outline: Assembly with a missing or undeclared input fails
    Given a template "answer@1" that declares input "question" and the segments
      | segment | kind | text         |
      | turn    | user | {{question}} |
    And the template is registered
    When "answer@1" is assembled with inputs <inputs>
    Then the assembly fails with code "invalid_argument" naming input "<input>"

    Examples:
      | inputs                                    | input    |
      | {}                                        | question |
      | {"question": "Why?", "language": "Latin"} | language |

  @REQ-PROMPT-006 @security @id:PROMPT-006-01
  Scenario: Template syntax inside a value is not expanded
    Given a template "pair@1" that declares trusted input "first" and trusted input "second" and the segments
      | segment | kind    | text                   |
      | context | dynamic | {{first}} / {{second}} |
    And the template is registered
    When "pair@1" is assembled with inputs {"first": "{{second}}", "second": "hunter2"}
    Then segment "context" reads "{{second}} / hunter2"

  @REQ-PROMPT-007 @id:PROMPT-007-01
  Scenario: The same inputs in a different order assemble the same prompt
    Given a template "pair@1" that declares trusted input "first" and trusted input "second" and the segments
      | segment | kind    | text                   |
      | context | dynamic | {{first}} / {{second}} |
    And the template is registered
    When "pair@1" is assembled with inputs {"first": "a", "second": "b"}
    And "pair@1" is assembled again with inputs {"second": "b", "first": "a"}
    Then both assembled prompts have the same text and the same content hash
