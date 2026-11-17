@planned @wp:WP-L10-01 @runtime:any @level:component
Feature: Codegen iteration
  "codegen.iterate" applies a change request to an existing repository over
  the same loop as "codegen.bootstrap", and lands its commit on a new branch.

  Background:
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And the preset is version "v1" with a repair guard of 3 visits and 5000 milli-points
    And "codegen.iterate" uses the scripted model and the simulated sandbox and git host

  @REQ-PRESET-012 @REQ-PRESET-013 @id:PRESET-012-01
  Scenario: An approved iteration commits on a new branch only
    Given repository "acme/app" has 3 commits on branch "main"
    And a change request whose plan has 1 module "parser" and plans, implements and verifies it
    And an iterate run of the change request on branch "main" is waiting at gate "review"
    When a person resolves gate "review" with "approved"
    Then the git host recorded 1 commit, on new branch "codegen/<run id>" from branch "main"
    And branch "main" is still at its 3rd commit

  @REQ-PRESET-012 @id:PRESET-012-02
  Scenario: A rejected iteration opens no branch
    Given repository "acme/app" has 3 commits on branch "main"
    And a change request whose plan has 1 module "parser" and plans, implements and verifies it
    And an iterate run of the change request on branch "main" is waiting at gate "review"
    When a person resolves gate "review" with "rejected"
    Then the run ends in state "cancelled"
    And the git host has no branch "codegen/<run id>"
    And the git host recorded 0 commits and 0 writer calls
