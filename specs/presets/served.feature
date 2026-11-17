@planned @wp:WP-L10-01 @runtime:any @level:system @profile:embedded
Feature: Codegen through the binary
  A codegen run of the embedded profile's server, from start to commit, with
  the scripted model, the simulated sandbox and the simulated git host.

  Background:
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And the preset is version "v1" with a repair guard of 3 visits and 5000 milli-points
    And "codegen.bootstrap" uses the scripted model and the simulated sandbox and git host

  @REQ-PRESET-002 @id:PRESET-002-02
  Scenario: The served run commits once on approval and records every stage
    Given "kit serve --profile embedded" runs and reports ready
    And a bootstrap brief whose plan has 2 modules and plans and implements them and verifies with 1 failing, then 0 failing, tests
    When a bootstrap run of brief "invoices" is started through the server and a person resolves gate "review" with "approved"
    Then the server reports the run's checkpoints for stages "plan", "implement", "verify", "repair" and "review"
    And the git host recorded 1 commit, on a new repository's default branch
    And no stage started twice in the run's events
