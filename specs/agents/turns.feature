@planned @wp:WP-L7-01 @runtime:any @level:component
Feature: One model turn per step

  Background:
    Given flow "worker" defines agent node "worker" with a scripted model

  @REQ-AGENT-001 @id:AGENT-001-01
  Scenario: Each model turn is one step
    Given the scripted model of "worker" answers "Done." in 1 turn
    When an agent run of "worker" completes
    Then the agent run has 1 model turn step
    And the result of step "turn-1" is the model output "Done."

  @REQ-AGENT-002 @id:AGENT-002-01
  Scenario: A resumed run feeds its completed turns from the record
    Given the scripted model of "worker" answers "Searching." and requests tool "search", then answers "Done." in 2 turns
    And an agent run of "worker" completed its model turn 1 and its tool call before the process stopped
    When the agent run resumes
    Then model turn 2 and the tool call of model turn 1 completed after the resume
    And the scripted model received 1 invocation after the process stopped

  @REQ-AGENT-003 @id:AGENT-003-01
  Scenario: A turn that requests two tools makes two tool calls
    Given the scripted model of "worker" requests tools "search" and "fetch", then answers "Done." in 2 turns
    When an agent run of "worker" completes
    Then the agent run has 2 tool-call steps
    And tool "search" was invoked once
    And tool "fetch" was invoked once

  @REQ-AGENT-004 @id:AGENT-004-01
  Scenario: A tool result is fed to the next turn
    Given the scripted model of "worker" requests tool "search", then answers "Done." in 2 turns
    And tool "search" returns "42"
    When an agent run of "worker" completes
    Then the messages of model turn 2 contain the result of tool "search": "42"

  @REQ-AGENT-005 @id:AGENT-005-01
  Scenario: A failed tool call fails the run
    Given the scripted model of "worker" requests tool "search" in 1 turn
    And tool "search" fails with code "timeout"
    When an agent run of "worker" runs
    Then the agent run fails with code "timeout"
    And the scripted model received 1 invocation after the process stopped

  @REQ-AGENT-006 @security @id:AGENT-006-01
  Scenario: A request for a tool the agent does not allow fails the run
    Given agent "worker" allows tools "search"
    And the scripted model of "worker" requests tool "shell" in 1 turn
    When an agent run of "worker" runs
    Then the agent run fails with code "permission_denied"
    And tool "shell" was invoked 0 times
    And an audit event records that agent "worker" requested tool "shell"

  @REQ-AGENT-007 @id:AGENT-007-01
  Scenario: An answer without a tool request ends the run
    Given the scripted model of "worker" requests tool "search", then answers "Done." in 2 turns
    When an agent run of "worker" completes
    Then the agent run is completed with output "Done."
    And the scripted model received 2 invocations
