@wp:WP-L9-01 @level:system
Feature: Browser runner contract
  These scenarios form the conformance suite every browser runner
  implementation must pass. They run against reference fixture apps.

  @REQ-BROWSER-001 @contract @id:BROWSER-001-01
  Scenario: A scripted session runs to completion as an invocation
    Given a browser runner under test and the fixture app "todo"
    When a scripted session is started for feature "todo/add-item.feature"
    Then the session reports progress events and ends with state "succeeded"
    And each step of the feature has a reported result

  @REQ-BROWSER-002 @id:BROWSER-002-01
  Scenario: The rrweb recording lines up with the actions
    Given a browser runner under test and the fixture app "todo"
    When a scripted session is started with recording "rrweb"
    Then the artifacts include an rrweb recording
    And every action event falls within the recording's time range

  @REQ-BROWSER-003 @security @id:BROWSER-003-01
  Scenario: Input values are masked by default
    Given a browser runner under test and the fixture app "login"
    When a scripted session types "s3cret" into the password field with recording "rrweb"
    Then the rrweb recording does not contain "s3cret"

  @REQ-BROWSER-004 @id:BROWSER-004-01
  Scenario: A failing step is reported with evidence
    Given a browser runner under test and the fixture app "todo" with a broken delete button
    When a scripted session is started for feature "todo/delete-item.feature"
    Then the step "When ana deletes the item" is reported failed
    And the failure carries a screenshot and the rrweb timestamp

  @REQ-BROWSER-005 @id:BROWSER-005-01
  Scenario: A goal-driven session stops at its step budget
    Given a browser runner under test, a scripted model on the platform endpoint, and the fixture app "todo"
    And the scripted model never declares the goal reached
    When a goal-driven session is started with goal "add two items" and a budget of 5 steps
    Then the session ends after 5 actions with state "failed" and reason "budget_exceeded"
    And every action is reported with its target and a screenshot

  @REQ-BROWSER-006 @id:BROWSER-006-01
  Scenario: The runner calls models with its session token
    Given a browser runner under test, a scripted model on the platform endpoint, and the fixture app "todo"
    When a goal-driven session is started with goal "add two items"
    Then every model call the runner made reached the platform endpoint with that session's token
