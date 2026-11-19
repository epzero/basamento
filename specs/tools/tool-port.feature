@wp:WP-L7-02 @level:component
Feature: Calling a tool
  A tool has a name, a description and an input schema. A call with arguments
  that do not satisfy the schema never reaches the tool, and the caller's model
  learns what was wrong. A call is one step that runs once, however the run
  resumes, and what comes back is a result or an error the model can act on.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"

  @REQ-TOOL-001 @id:TOOL-001-01
  Scenario: A tool is known by its name, description and input schema
    Given a flow "shipping" whose tool node calls tool "track-parcel"
    And tool "track-parcel" has the description "Finds the latest position of a parcel" and an input schema requiring "tracking_number"
    When the tool layer describes tool "track-parcel"
    Then the description is "Finds the latest position of a parcel"
    And the input schema requires "tracking_number"
    And the schema's properties are "tracking_number" only

  @REQ-TOOL-023 @id:TOOL-023-01
  Scenario: A model sees only the tools on its caller's allowlist
    Given agent "assistant" is given tools "search" and "fetch"
    And tool "delete_file" exists
    When agent "assistant" starts a model turn
    Then the model is offered the tools "fetch" and "search"

  @REQ-TOOL-002 @id:TOOL-002-01
  Scenario: Arguments that do not satisfy the schema are never passed to the tool
    Given agent "assistant" is given tool "track-parcel", whose input schema requires "tracking_number"
    When agent "assistant" calls tool "track-parcel" with arguments {}
    Then tool "track-parcel" was not run
    And the call ends with code "invalid_argument"
    And the error names "tracking_number" as required

  @REQ-TOOL-002 @id:TOOL-002-02
  Scenario Outline: A wrong argument is reported with what the schema requires
    Given agent "assistant" is given tool "track-parcel", whose input schema requires "tracking_number" of type "string" and allows no other properties
    When agent "assistant" calls tool "track-parcel" with arguments <arguments>
    Then tool "track-parcel" was not run
    And the call ends with code "invalid_argument"
    And the error names "<argument>" as <requirement>

    Examples:
      | arguments                                 | argument        | requirement      |
      | {"tracking_number": 42}                   | tracking_number | must be a string |
      | {"tracking_number": "P-1", "mode": "air"} | mode            | not allowed      |

  @REQ-TOOL-003 @id:TOOL-003-01
  Scenario: A run that resumes after completing a call does not run the tool again
    Given a flow "shipping" whose tool node calls tool "place-order"
    And tool "place-order" succeeds with the result {"order_id": "O-1"}
    And the run of flow "shipping" ran the call and was interrupted before its next step
    When the run is resumed
    Then the resumed run receives the recorded result {"order_id": "O-1"}
    And tool "place-order" was run 1 time

  @REQ-TOOL-003 @id:TOOL-003-02
  Scenario: Calls of one run each have a step of their own
    Given agent "assistant" is given tool "search"
    When agent "assistant" calls tool "search" twice in one run
    Then tool "search" was run 2 times
    And the 2 calls ran as 2 steps with different idempotency keys

  @REQ-TOOL-003 @id:TOOL-003-03
  Scenario: A run that resumes after a failed call returns the recorded error without running the tool again
    Given a flow "shipping" whose tool node calls tool "place-order"
    And tool "place-order" returns the error "card declined" with code "vendor_error"
    And the run of flow "shipping" ran the call and was interrupted before its next step
    When the run is resumed
    Then the resumed run receives the recorded error with code "vendor_error" and message "card declined"
    And tool "place-order" was run 1 time

  @REQ-TOOL-004 @id:TOOL-004-01
  Scenario: A tool's error ends the call without a retry
    Given agent "assistant" is given tool "search"
    And tool "search" returns the error "upstream index is rebuilding" with code "vendor_error" and marks it retryable
    When agent "assistant" calls tool "search"
    Then the call ends with code "vendor_error" and message "upstream index is rebuilding"
    And tool "search" was run 1 time
    And the run state is "running"

  @REQ-TOOL-005 @id:TOOL-005-01
  Scenario: A tool that panics ends the call with an internal error
    Given agent "assistant" is given tool "crawl"
    And tool "crawl" panics when it runs
    When agent "assistant" calls tool "crawl"
    Then the call ends with code "internal"
    And the call's message names "crawl"
    And the run state is "running"

  @REQ-TOOL-006 @id:TOOL-006-01
  Scenario: A tool that exceeds its time limit is stopped at its next heartbeat
    Given agent "assistant" is given tool "slow-report", whose time limit is 30 seconds and which reports progress every second
    And tool "slow-report" runs for 60 seconds
    When the clock advances by 30 seconds
    Then the call ends with code "timeout"
    And after 1 more second tool "slow-report" was stopped
    And tool "slow-report" was run 1 time

  @REQ-TOOL-007 @id:TOOL-007-01
  Scenario: A result over the size limit comes back as a reference
    Given agent "assistant" is given tool "dump", whose result size limit is 1 KiB
    And tool "dump" returns a result of 2048 bytes
    When agent "assistant" calls tool "dump"
    Then the call succeeds
    And the result is a blob reference and not the result's bytes
    And blob storage holds the 2048 bytes at that reference

  @REQ-TOOL-008 @security @id:TOOL-008-01
  Scenario: An agent call to a tool outside its allowlist is refused and never run
    Given agent "assistant" is given tool "search"
    And tool "delete_file" exists and is registered
    When agent "assistant" calls tool "delete_file"
    Then the call ends with code "permission_denied"
    And tool "delete_file" was not run

  @REQ-TOOL-008 @security @id:TOOL-008-02
  Scenario: A flow call to a tool outside its allowlist is refused and never run
    Given a flow "cleanup" is given tool "search" and its tool node calls tool "delete_file"
    And tool "delete_file" exists and is registered
    When a run of flow "cleanup" executes the tool node
    Then the call ends with code "permission_denied"
    And tool "delete_file" was not run

  @REQ-TOOL-009 @id:TOOL-009-01
  Scenario: A tool removed from the allowlist disappears from the next turn
    Given agent "assistant" is given tools "search" and "fetch"
    And agent "assistant" has completed 1 model turn offered tools "fetch" and "search"
    When tool "search" is removed from agent "assistant"'s allowlist
    And agent "assistant" starts a model turn
    Then the model is offered the tools "fetch"

  @REQ-TOOL-011 @id:TOOL-011-01
  Scenario: A model learns why a call failed, without the tool's details
    Given agent "assistant" is given tool "search"
    And tool "search" returns the error "upstream index is rebuilding" with code "vendor_error" and details {"host": "index-7.internal"}
    When agent "assistant" calls tool "search" and starts a model turn
    Then the model receives the error code "vendor_error" and message "upstream index is rebuilding"
    And the model receives no details

  @REQ-TOOL-011 @security @id:TOOL-011-02
  Scenario: A refused call teaches the model the tool is not its own
    Given agent "assistant" is given tool "search"
    And tool "delete_file" exists and is registered
    When agent "assistant" calls tool "delete_file" and starts a model turn
    Then the model receives the error code "permission_denied"
    And tool "delete_file" was not run
