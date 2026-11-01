@wp:WP-L0-01 @level:component
Feature: Entity identifiers
  Identifiers name every persisted entity. They must never collide, even after
  a restart, and they sort by creation time so lists and cursors are stable.

  @REQ-CORE-001 @id:CORE-001-01
  Scenario: Identifiers stay unique across a restart within the same millisecond
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And an id generator
    When 1000 ids of kind "run" are generated
    And the id generator restarts
    And 1000 more ids of kind "run" are generated
    Then all 2000 ids are distinct

  @REQ-CORE-002 @id:CORE-002-01
  Scenario: A later identifier sorts after an earlier one
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    When an id of kind "run" is generated as "first"
    And the clock advances by 1 millisecond
    And an id of kind "run" is generated as "second"
    Then id "first" sorts before id "second"

  @REQ-CORE-002 @id:CORE-002-02
  Scenario: Identifiers issued within one millisecond still sort in issue order
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    When an id of kind "run" is generated as "first"
    And an id of kind "run" is generated as "second"
    Then id "first" sorts before id "second"

  @REQ-CORE-003 @id:CORE-003-01
  Scenario: An identifier carries its kind
    When an id of kind "episode" is generated as "e"
    Then id "e" is "episode_" followed by 26 Crockford base32 characters

  @REQ-CORE-004 @id:CORE-004-01
  Scenario Outline: Invalid kinds are rejected
    When an id of kind "<kind>" is requested
    Then the request fails with code "invalid_argument"

    Examples:
      | kind                  |
      |                       |
      | Run                   |
      | run-1                 |
      | 1run                  |
      | averyveryverylongkind |
