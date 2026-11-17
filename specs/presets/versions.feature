@planned @wp:WP-L10-01 @runtime:any @level:component
Feature: Preset versions
  A new version of the codegen preset is accepted only when it compiles, its
  `@eval` suite does not regress and a person resolves its review gate. The
  eval scores used here are recorded judge scores, so the gate stays
  deterministic; case ids, scores and the judge contract are in `design.md`.

  Background:
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And an `@eval` suite with cases "plan-modules" and "iterate-minimal"

  @REQ-PRESET-016 @id:PRESET-016-01
  Scenario: A version that does not compile is rejected before any eval
    Given preset version "v1" is accepted with recorded case scores 82 and 75
    And preset version "v2" has a prompt reference that does not compile
    When version "v2" is proposed as the next version
    Then version "v2" is rejected with code "invalid_argument"
    And the version's review gate is never opened
    And the `@eval` suite ran 0 cases

  @REQ-PRESET-017 @id:PRESET-017-01
  Scenario: One lower case score rejects the version
    Given preset version "v1" is accepted with recorded case scores 82 and 75
    And preset version "v2" compiles and has recorded case scores 85 and 74
    When version "v2" is proposed as the next version
    Then version "v2" is rejected with code "conflict" naming case "iterate-minimal"
    And version "v1" is still the current version

  @REQ-PRESET-017 @id:PRESET-017-02
  Scenario: Equal scores do not regress
    Given preset version "v1" is accepted with recorded case scores 82 and 75
    And preset version "v2" compiles and has recorded case scores 82 and 75
    When version "v2" is proposed as the next version
    Then the version's review gate is open

  @REQ-PRESET-017 @id:PRESET-017-03
  Scenario: A case the suite did not run counts as a regression
    Given preset version "v1" is accepted with recorded case scores 82 and 75
    And preset version "v2" compiles and has a recorded case score 82
    When version "v2" is proposed as the next version
    Then version "v2" is rejected with code "conflict" naming case "iterate-minimal"

  @REQ-PRESET-018 @id:PRESET-018-01
  Scenario: A person approves the review gate and the version becomes current
    Given preset version "v1" is accepted with recorded case scores 82 and 75
    And preset version "v2" compiles and has recorded case scores 85 and 80
    And version "v2" is waiting at its review gate
    When a person resolves the review gate of version "v2" with "approved"
    Then version "v2" is accepted and is the current version
    And version "v1" is superseded

  @REQ-PRESET-018 @id:PRESET-018-02
  Scenario: A rejected review gate leaves the version unaccepted
    Given preset version "v1" is accepted with recorded case scores 82 and 75
    And preset version "v2" compiles and has recorded case scores 85 and 80
    And version "v2" is waiting at its review gate
    When a person resolves the review gate of version "v2" with "rejected"
    Then version "v2" is rejected with code "cancelled"
    And version "v1" is still the current version
