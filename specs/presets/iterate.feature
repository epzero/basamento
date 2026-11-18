@planned @wp:WP-L10-01 @runtime:any @level:component
Feature: Codegen iteration
  "codegen.iterate" applies a change request to an existing repository over
  the same loop as "codegen.bootstrap", and lands its commit on a new branch.
  No person decides before that commit.

  Background:
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And the preset is version "v1" with a repair guard of 3 visits and 5000 milli-points and a self-review guard of 3 visits
    And "codegen.iterate" uses the scripted model and the simulated sandbox and git host

  @REQ-PRESET-012 @id:PRESET-012-01
  Scenario: An iteration commits on a new branch only
    Given repository "acme/app" has 3 commits on default branch "main" and a branch "release/v1" at its 3rd commit
    And a change request whose plan has 1 module "parser" and plans and implements it and verifies once
    When an iterate run of the change request on branch "release/v1" is started
    Then the run completes
    And the git host recorded 1 commit, on new branch "codegen/<run id>" from branch "release/v1"
    And branch "release/v1" is still at its 3rd commit
    And a person was asked for no decision and no reviewer session was started

  @REQ-PRESET-012 @REQ-PRESET-020 @cost @id:PRESET-012-02
  Scenario: An iteration whose self-review findings never clear opens no branch
    Given repository "acme/app" has 3 commits on default branch "main" and a branch "release/v1" at its 3rd commit
    And a change request whose plan has 1 module "parser" and plans, implements and verifies it
    And the self-review reports 1 finding every time
    When an iterate run of the change request on branch "release/v1" is started
    Then the run ends in state "failed" with code "budget_exceeded"
    And the self-review stage ran 3 times
    And the git host has no branch "codegen/<run id>"
    And the git host recorded 0 commits and 0 writer calls

  @REQ-PRESET-013 @id:PRESET-013-01
  Scenario: An iteration targeting the default branch leaves the default branch untouched
    Given repository "acme/app" has 3 commits on default branch "main"
    And a change request whose plan has 1 module "parser" and plans and implements it and verifies once
    When an iterate run of the change request on branch "main" is started
    Then the run completes
    And the git host recorded 1 commit, on new branch "codegen/<run id>" from branch "main"
    And default branch "main" is still at its 3rd commit
