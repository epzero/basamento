@wp:WP-L10-01 @runtime:any @level:component
Feature: Generated code quality
  `@eval` scenarios: an LLM judge scores, over N runs, the outputs a codegen
  run produces for a fixture. Nightly scores live models; the PR gate replays
  recorded judge scores. Fixtures are in `design.md`.

  @REQ-PRESET-019 @eval @id:PRESET-019-01
  Scenario: Plans cover the brief's modules
    Given eval suite "codegen" and case "plan-modules" with fixture "expenses-app"
    When the judge scores the plan and implement prompts the codegen flow builds over 5 runs and the plans they produce
    Then the mean module coverage score is at least 80
    And the mean prompt-quality score is at least 75

  @REQ-PRESET-019 @eval @id:PRESET-019-02
  Scenario: Iterations do not rewrite the repository
    Given eval suite "codegen" and case "iterate-minimal" with fixture "cli-tool"
    And a change request that adds one command
    When the judge scores the workspace the iterate flow commits for the request over 5 runs
    Then the mean score for changing only the requested command is at least 80
    And the mean score for leaving unrelated files untouched is at least 90
