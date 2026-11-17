@planned @wp:WP-L10-01 @runtime:any @level:component
Feature: The codegen loop
  Both codegen flows run one loop: plan, implement each planned module, verify
  in a sandbox, repair under a guard, wait at the review gate, commit. The
  scripted model writes the plan and returns a scripted sandbox workspace in
  every stage after it; the simulated sandbox runs the plan's tests on that
  workspace and returns the scripted result.

  Background:
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And the preset is version "v1" with a repair guard of 3 visits and 5000 milli-points
    And "codegen.bootstrap" uses the scripted model and the simulated sandbox and git host

  @REQ-PRESET-001 @id:PRESET-001-01
  Scenario: Planning comes before everything the plan needs
    Given a bootstrap brief whose plan has no module
    When a bootstrap run of brief "invoices" is started
    Then the stage log is ["plan"]
    And the scripted model received 1 request

  @REQ-PRESET-002 @id:PRESET-002-01
  Scenario: A completed stage leaves a checkpoint
    Given a bootstrap brief whose plan has 2 modules and plans, implements and verifies each
    When a bootstrap run of brief "invoices" is started
    Then the run has checkpoints for stages "plan" and "implement" and not "verify"

  @REQ-PRESET-004 @id:PRESET-004-01
  Scenario: One implement step per planned module, in plan order
    Given a bootstrap brief whose plan has 3 modules "store", "api" and "cli", in that order, and plans, implements and verifies each
    When a bootstrap run of brief "invoices" is started
    Then the stage log is ["plan", "implement", "verify", "review"]
    And the scripted model received 3 requests to implement a module
    And the implement requests name modules "store", "api" and "cli", in that order

  @REQ-PRESET-005 @id:PRESET-005-01
  Scenario: The plan's tests run once every module is implemented
    Given a bootstrap brief whose plan has 2 modules and plans and implements them and verifies once
    When a bootstrap run of brief "invoices" is started
    Then the sandbox received 2 calls
    And both calls ran on the workspace the model returned in stage "implement"

  @REQ-PRESET-006 @id:PRESET-006-01
  Scenario: A failing verification is repaired
    Given a bootstrap brief whose plan has 1 module "store" and plans and implements it and verifies with 1 failing, then 0 failing, tests
    When a bootstrap run of brief "invoices" is started
    Then the stage log is ["plan", "implement", "verify", "repair", "verify", "review"]

  @REQ-PRESET-008 @id:PRESET-008-01
  Scenario: A repair that passes the tests goes to a person
    Given a bootstrap brief whose plan has 1 module "store" and plans and implements it and verifies with 1 failing, then 0 failing, tests
    When a bootstrap run of brief "invoices" is started
    Then the run waits at gate "review"
    And the repair stage ran once, with the output of the failing verification

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
    And a repair visit costs 2000 milli-points
    When a bootstrap run of brief "invoices" is started
    Then the run ends in state "failed" with code "budget_exceeded"
    And the repair stage ran 2 times
    And the run names the limit "cost" and the last test failures "tests fail"

  @REQ-PRESET-009 @id:PRESET-009-01
  Scenario: An open review writes nothing
    Given a bootstrap brief whose plan has 1 module "store" and plans, implements and verifies it
    When a bootstrap run of brief "invoices" is started
    Then the run waits at gate "review"
    And the git host recorded 0 commits and 0 writer calls

  @REQ-PRESET-010 @id:PRESET-010-01
  Scenario: An approved review makes one commit, the first of a new repository
    Given a bootstrap brief whose plan has 1 module "store" and plans, implements and verifies it
    And a bootstrap run of brief "invoices" is waiting at gate "review"
    When a person resolves gate "review" with "approved"
    Then the run completes
    And the git host recorded 1 commit, on a new repository's default branch, with the reviewed workspace

  @REQ-PRESET-011 @id:PRESET-011-01
  Scenario: A rejected review commits nothing
    Given a bootstrap brief whose plan has 1 module "store" and plans, implements and verifies it
    And a bootstrap run of brief "invoices" is waiting at gate "review"
    When a person resolves gate "review" with "rejected"
    Then the run ends in state "cancelled"
    And the git host recorded 0 commits

  @REQ-PRESET-014 @id:PRESET-014-01
  Scenario: A run that saw two versions uses the first
    Given a bootstrap brief whose plan has 1 module "store" and plans and implements it and verifies with 1 failing, then 0 failing, tests
    And a bootstrap run of brief "invoices" is paused after its first verification
    When preset version "v2" replaces version "v1" as the current version
    And the run resumes and a person resolves gate "review" with "approved"
    Then the repair stage ran with the prompt of version "v1"
    And the run's version is "v1"

  @REQ-PRESET-015 @id:PRESET-015-01
  Scenario: A run's events name the version and its inputs
    Given a bootstrap brief whose plan has 1 module "store" and plans, implements and verifies it
    When a bootstrap run of brief "invoices" is started with the preset's analysis bundle
    Then the run's run-started event names flow "codegen.bootstrap", version "v1", prompt hashes "v1/prompts" and bundle "bundle-v1"

  @REQ-PRESET-003 @id:PRESET-003-01
  Scenario Outline: A resumed run does not redo a completed stage
    Given a bootstrap brief whose plan has 2 modules and plans, implements and verifies each
    And a bootstrap run of brief "invoices" is stopped after stage <stage>
    When the run resumes it
    Then the stage log is ["plan", "implement", "verify", "review"]
    And the model made 3 plan and implement requests and 1 repair request in both attempts together
    And the sandbox made 1 call in both attempts together

    Examples:
      | stage     |
      | plan      |
      | implement |
      | verify    |
