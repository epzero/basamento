@planned @wp:WP-L3-05 @level:component
Feature: Segments, budgets and cache hints
  The assembler orders a template's segments so the stable text comes first,
  keeps each segment within its budget, trims history from the oldest entry
  and marks where the cacheable prefix ends. Sizes count characters.

  @REQ-PROMPT-008 @id:PROMPT-008-01
  Scenario: Segments are assembled by kind, whatever the order they are declared in
    Given a template "chat@1" that declares trusted input "tenant_name" and input "question" and the segments
      | segment | kind        | text                 |
      | turn    | user        | {{question}}         |
      | notes   | dynamic     | No notes.            |
      | rules   | static      | Be brief.            |
      | tenant  | semi-static | For {{tenant_name}}. |
      | style   | static      | Use English.         |
      | past    | history     |                      |
    And the template is registered
    When "chat@1" is assembled with inputs {"tenant_name": "Acme", "question": "Why?"} and no history
    Then the assembled segments are, in order, "rules", "style", "tenant", "notes", "past", "turn"

  @REQ-PROMPT-009 @id:PROMPT-009-01
  Scenario: A segment larger than its budget fails the assembly
    Given a template "docs@1" that declares input "doc" and the segments
      | segment | kind    | budget | text    |
      | context | dynamic | 10     | {{doc}} |
    And the template is registered
    When "docs@1" is assembled with inputs {"doc": "hello world"}
    Then the assembly fails with code "invalid_argument" naming segment "context" with size 11 and budget 10

  @REQ-PROMPT-009 @id:PROMPT-009-02
  Scenario: A segment exactly at its budget is assembled
    Given a template "docs@1" that declares input "doc" and the segments
      | segment | kind    | budget | text    |
      | context | dynamic | 10     | {{doc}} |
    And the template is registered
    When "docs@1" is assembled with inputs {"doc": "hello worl"}
    Then the assembly succeeds with segment "context" of size 10

  @REQ-PROMPT-010 @id:PROMPT-010-01
  Scenario: History over its budget loses its oldest entries first
    Given a template "chat@1" that declares input "question" and the segments
      | segment | kind    | budget | text         |
      | past    | history | 20     |              |
      | turn    | user    | 100    | {{question}} |
    And the template is registered
    When "chat@1" is assembled with inputs {"question": "Why?"} and the history
      | role      | text     |
      | user      | entry-01 |
      | assistant | entry-02 |
      | user      | entry-03 |
    Then the history segment holds the entries "entry-02", "entry-03"
    And the assembled prompt reports 1 history entry dropped

  @REQ-PROMPT-010 @id:PROMPT-010-02
  Scenario: History is trimmed to fit the total budget
    Given a template "chat@1" with a total budget of 30 that declares input "question" and the segments
      | segment | kind    | budget | text         |
      | rules   | static  | 20     | Be brief.    |
      | past    | history | 100    |              |
      | turn    | user    | 20     | {{question}} |
    And the template is registered
    When "chat@1" is assembled with inputs {"question": "Why?"} and the history
      | role      | text     |
      | user      | entry-01 |
      | assistant | entry-02 |
      | user      | entry-03 |
    Then the history segment holds the entries "entry-02", "entry-03"
    And the assembled prompt has size 29 and reports 1 history entry dropped

  @REQ-PROMPT-010 @id:PROMPT-010-03
  Scenario: History within its budget is kept whole
    Given a template "chat@1" that declares input "question" and the segments
      | segment | kind    | budget | text         |
      | past    | history | 24     |              |
      | turn    | user    | 100    | {{question}} |
    And the template is registered
    When "chat@1" is assembled with inputs {"question": "Why?"} and the history
      | role      | text     |
      | user      | entry-01 |
      | assistant | entry-02 |
      | user      | entry-03 |
    Then the history segment holds the entries "entry-01", "entry-02", "entry-03"
    And the assembled prompt reports 0 history entries dropped

  @REQ-PROMPT-011 @id:PROMPT-011-01
  Scenario: A prompt over its total budget without history to drop fails
    Given a template "chat@1" with a total budget of 12 that declares input "question" and the segments
      | segment | kind    | budget | text         |
      | rules   | static  | 20     | Be brief.    |
      | past    | history | 100    |              |
      | turn    | user    | 20     | {{question}} |
    And the template is registered
    When "chat@1" is assembled with inputs {"question": "Why?"} and the history
      | role | text     |
      | user | entry-01 |
    Then the assembly fails with code "invalid_argument" naming the total budget 12 and size 13

  @REQ-PROMPT-012 @id:PROMPT-012-01
  Scenario: The cache hint follows the last static or semi-static segment
    Given a template "chat@1" that declares trusted input "tenant_name" and input "question" and the segments
      | segment | kind        | text                 |
      | rules   | static      | Be brief.            |
      | tenant  | semi-static | For {{tenant_name}}. |
      | notes   | dynamic     | No notes.            |
      | turn    | user        | {{question}}         |
    And the template is registered
    When "chat@1" is assembled with inputs {"tenant_name": "Acme", "question": "Why?"} and no history
    Then the assembled prompt has 1 cache hint, after segment "tenant"

  @REQ-PROMPT-012 @id:PROMPT-012-02
  Scenario: A prompt without static or semi-static segments has no cache hint
    Given a template "bare@1" that declares input "question" and the segments
      | segment | kind    | text         |
      | notes   | dynamic | No notes.    |
      | turn    | user    | {{question}} |
    And the template is registered
    When "bare@1" is assembled with inputs {"question": "Why?"} and no history
    Then the assembled prompt has 0 cache hints and an empty prefix hash

  @REQ-PROMPT-013 @id:PROMPT-013-01
  Scenario: Different questions and history keep the same cacheable prefix
    Given a template "chat@1" that declares trusted input "tenant_name" and input "question" and the segments
      | segment | kind        | text                 |
      | rules   | static      | Be brief.            |
      | tenant  | semi-static | For {{tenant_name}}. |
      | past    | history     |                      |
      | turn    | user        | {{question}}         |
    And the template is registered
    When "chat@1" is assembled with inputs {"tenant_name": "Acme", "question": "Why?"} and no history
    And "chat@1" is assembled again with inputs {"tenant_name": "Acme", "question": "How?"} and 2 history entries
    Then both assembled prompts have the same prefix hash and different content hashes

  @REQ-PROMPT-013 @id:PROMPT-013-02
  Scenario: A different semi-static input changes the cacheable prefix
    Given a template "chat@1" that declares trusted input "tenant_name" and input "question" and the segments
      | segment | kind        | text                 |
      | rules   | static      | Be brief.            |
      | tenant  | semi-static | For {{tenant_name}}. |
      | turn    | user        | {{question}}         |
    And the template is registered
    When "chat@1" is assembled with inputs {"tenant_name": "Acme", "question": "Why?"} and no history
    And "chat@1" is assembled again with inputs {"tenant_name": "Globex", "question": "Why?"} and no history
    Then the two assembled prompts have different prefix hashes
