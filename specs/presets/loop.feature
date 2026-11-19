@wp:WP-L10-01 @runtime:any @level:component
Feature: The codegen loop
  Both codegen flows run one loop: plan, implement each planned module, verify
  in a sandbox, repair under a guard, self-review, commit. The loop mirrors the
  developer's own workflow; it starts no reviewer session and no person decides
  before the commit. The scripted model writes the plan and returns a scripted
  sandbox workspace in every stage after it; the simulated sandbox runs the
  plan's tests on that workspace and returns the scripted result; the
  simulated self-review returns its scripted findings.

  Background:
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And the preset is version "v1" with a repair guard of 3 visits and 5000 milli-points and a self-review guard of 3 visits
    And "codegen.bootstrap" uses the scripted model and the simulated sandbox and git host

  @REQ-PRESET-001 @id:PRESET-001-01
  Scenario: Planning comes before everything the plan needs
    Given a bootstrap brief whose plan has 1 module "store" and plans, implements and verifies it
    And a bootstrap run of brief "invoices" is holding before stage "plan"
    When the hold before stage "plan" is released
    Then the stage log is ["plan", "implement", "verify", "self-review", "commit"]
    And the scripted model received 0 implement requests before stage "plan" completed

  @REQ-PRESET-002 @id:PRESET-002-01
  Scenario: A completed stage leaves a checkpoint
    Given a bootstrap brief whose plan has 2 modules and plans and implements them and verifies once
    When a bootstrap run of brief "invoices" is started
    Then the run has checkpoints for stages "plan", "implement", "verify", "self-review" and "commit" and not "repair"

  @REQ-PRESET-004 @id:PRESET-004-01
  Scenario: One implement step per planned module, in plan order
    Given a bootstrap brief whose plan has 3 modules "store", "api" and "cli", in that order, and plans and implements them and verifies once
    When a bootstrap run of brief "invoices" is started
    Then the stage log is ["plan", "implement", "verify", "self-review", "commit"]
    And the scripted model received 3 requests to implement a module
    And the implement requests name modules "store", "api" and "cli", in that order

  @REQ-PRESET-005 @id:PRESET-005-01
  Scenario: The plan's tests run once every module is implemented
    Given a bootstrap brief whose plan has 2 modules and plans and implements them and verifies once
    When a bootstrap run of brief "invoices" is started
    Then the stage log is ["plan", "implement", "verify", "self-review", "commit"]
    And the sandbox received 1 call
    And the call ran on the workspace the model returned in stage "implement"

  @REQ-PRESET-006 @id:PRESET-006-01
  Scenario: A failing verification is repaired
    Given a bootstrap brief whose plan has 1 module "store" and plans and implements it and verifies with 2 failing, then 1 failing, then 0 failing, tests
    When a bootstrap run of brief "invoices" is started
    Then the stage log is ["plan", "implement", "verify", "repair", "verify", "repair", "verify", "self-review", "commit"]

  @REQ-PRESET-008 @id:PRESET-008-01
  Scenario: A repair that passes the tests goes to the author's self-review
    Given a bootstrap brief whose plan has 1 module "store" and plans and implements it and verifies with 1 failing, then 0 failing, tests
    When a bootstrap run of brief "invoices" is started
    Then the stage log is ["plan", "implement", "verify", "repair", "verify", "self-review", "commit"]
    And the repair stage ran once, with the output of the failing verification
    And a person was asked for no decision and no reviewer session was started

  @REQ-PRESET-007 @cost @id:PRESET-007-01
  Scenario: The visit limit ends a repair loop that never passes
    Given a bootstrap brief whose plan has 1 module "store" and plans and implements it and verifies "tests fail" every time
    When a bootstrap run of brief "invoices" is started
    Then the run ends in state "failed" with code "budget_exceeded"
    And the repair stage ran 3 times
    And the run names the limit "visits" and the last test failures "tests fail"

  @REQ-PRESET-007 @cost @id:PRESET-007-02
  Scenario: The cost limit ends a repair loop before its visit limit
    Given a bootstrap brief whose plan has 1 module "store" and plans and implements it and verifies "tests fail" every time
    And a repair visit costs 2500 milli-points
    When a bootstrap run of brief "invoices" is started
    Then the run ends in state "failed" with code "budget_exceeded"
    And the repair stage ran 2 times
    And the run names the limit "cost" and the last test failures "tests fail"

  @REQ-PRESET-009 @id:PRESET-009-01
  Scenario: Self-review findings go back to repair
    Given a bootstrap brief whose plan has 1 module "store" and plans, implements and verifies it
    And the self-review reports 1 finding, then none
    When a bootstrap run of brief "invoices" is started
    Then the stage log is ["plan", "implement", "verify", "self-review", "repair", "verify", "self-review", "commit"]
    And the repair stage's first run received the finding of the first self-review

  @REQ-PRESET-020 @cost @id:PRESET-020-01
  Scenario: The visit limit ends a self-review whose findings never clear
    Given a bootstrap brief whose plan has 1 module "store" and plans, implements and verifies it
    And the self-review reports 1 finding every time
    When a bootstrap run of brief "invoices" is started
    Then the run ends in state "failed" with code "budget_exceeded"
    And the self-review stage ran 3 times
    And the run names the limit "visits"
    And the git host recorded 0 commits and 0 writer calls

  @REQ-PRESET-010 @id:PRESET-010-01
  Scenario: A bootstrap run that passes its tests and self-review makes one commit, the first of a new repository
    Given a bootstrap brief whose plan has 1 module "store" and plans and implements it and verifies with 1 failing, then 0 failing, tests
    When a bootstrap run of brief "invoices" is started
    Then the run completes
    And the git host recorded 1 commit, on a new repository's default branch, with the workspace the last verification tested

  @REQ-PRESET-011 @id:PRESET-011-01
  Scenario: No writer is called before the commit stage
    Given a bootstrap brief whose plan has 1 module "store" and plans, implements and verifies it
    And a bootstrap run of brief "invoices" is holding before stage "commit"
    Then the git host recorded 0 commits and 0 writer calls
    When the hold before stage "commit" is released
    Then the git host recorded 1 commit at stage "commit"

  @REQ-PRESET-014 @id:PRESET-014-01
  Scenario: A run that saw two versions uses the first
    Given a bootstrap brief whose plan has 1 module "store" and plans and implements it and verifies with 1 failing, then 0 failing, tests
    And a bootstrap run of brief "invoices" is paused after its first verification
    When preset version "v2" replaces version "v1" as the current version
    And the run resumes
    Then the repair stage ran with the prompt of version "v1"
    And the run's version is "v1"

  @REQ-PRESET-015 @id:PRESET-015-01
  Scenario: A run's events name the version and its inputs
    Given a bootstrap brief whose plan has 1 module "store" and plans, implements and verifies it
    When a bootstrap run of brief "invoices" is started
    Then the run's run-started event names flow "codegen.bootstrap", version "v1", prompts "v1/prompts" and evals "v1/evals"

  @REQ-PRESET-003 @id:PRESET-003-01
  Scenario Outline: A resumed run does not redo a completed stage
    Given a bootstrap brief whose plan has 2 modules and plans and implements them and verifies once
    And a bootstrap run of brief "invoices" is stopped after stage "<stage>"
    When the run resumes and completes
    Then the stage log is ["plan", "implement", "verify", "self-review", "commit"]
    And the model made 3 plan and implement requests, 1 self-review request and 0 repair requests in both attempts together
    And the sandbox made 1 call in both attempts together
    And the git host recorded 1 commit in both attempts together

    Examples:
      | stage       |
      | plan        |
      | implement   |
      | verify      |
      | self-review |
