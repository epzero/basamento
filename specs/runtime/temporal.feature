@planned @wp:WP-L5-02 @runtime:temporal @level:component
Feature: The Temporal runtime

  The temporal runtime runs the flow host as a Temporal workflow and each step
  as an activity, with a payload codec that stores large history payloads as
  blobs and encrypts every history payload. What every runtime shares is in
  Conformance (WP-L5-04) and in the local runtime's scenarios (WP-L5-01); each
  scenario below checks one behavior of `runtime/temporal` (WP-L5-02).

  Background:
    Given a Temporal test server at version "1.32.2"
    And a blob store and a payload codec served by a simulated key service

  @REQ-RT-039 @id:RT-039-01
  Scenario: The temporal runtime reports its capabilities
    Given the runtime is "temporal"
    Then the runtime reports capability "durable" as true
    And the runtime reports capability "sync_gate_updates" as true
    And the runtime reports capability "continue_as_new" as true
    And the runtime reports capability "polyglot_queues" as true

  @REQ-RT-041 @id:RT-041-01
  Scenario: A retryable step that never succeeds is tried exactly its attempt limit
    Given step "charge" fails with code "vendor_error" marked retryable true
    And the step allows 2 attempts
    When a run of "pay" on the temporal runtime runs for 1 day of test time
    Then step "charge" was executed 2 times
    And the step fails with code "vendor_error"
    And the run is "failed"

  @REQ-RT-041 @id:RT-041-02
  Scenario: A retryable step that succeeds on its last allowed attempt lets the run complete
    Given step "charge" fails with code "vendor_error" marked retryable true on its first 3 executions
    And the step allows 4 attempts
    When a run of "pay" on the temporal runtime runs for 1 day of test time
    Then step "charge" was executed 4 times
    And the run is "completed"

  @REQ-RT-028 @id:RT-028-01
  Scenario Outline: A payload of <size> bytes with a claim-check threshold of 5 KB produces <blobs> claim-checked payloads
    Given the payload codec claim-check threshold is 5 KB
    And a flow calls step "fetch" whose result is <size> bytes of seed 7 and returns "done"
    When a run of "fetch" on the temporal runtime completes
    Then the run history holds <blobs> claim-checked payloads
    And every payload in the run history is at most 5 KB
    And the run returns "done"

    Examples:
      | size  | blobs |
      | 1024  | 0     |
      | 6000  | 1     |
      | 65536 | 1     |

  @REQ-RT-029 @id:RT-029-01
  Scenario: A claim-checked payload is restored unchanged
    Given the payload codec claim-check threshold is 5 KB
    And a flow calls step "fetch" whose result is 8192 bytes of seed 11 and returns it
    When a run of "fetch" on the temporal runtime completes
    Then the run history holds 2 claim-checked payloads
    And the run returns 8192 bytes of seed 11

  @security @REQ-RT-031 @id:RT-031-01
  Scenario: A step result in history is encrypted for the run's tenant and holds nothing readable
    Given a flow calls step "fetch" whose result is {"api_key": "sk-live-4f9a2c"} and returns it
    When a run of "fetch" for tenant "acme" on the temporal runtime completes
    Then the run history holds at least 1 payload, and every payload has encoding "binary/encrypted"
    And the run history holds neither "sk-live-4f9a2c" nor "json/plain"
    And the run returns {"api_key": "sk-live-4f9a2c"}

  @security @REQ-RT-040 @REQ-RT-031 @id:RT-031-02
  Scenario: A run without a tenant fails before any payload reaches the history unencrypted
    Given a flow calls step "fetch" that returns {"ok": true}
    When a run of "fetch" without a tenant is started on the temporal runtime
    Then starting the run fails with code "invalid_argument"
    And step "fetch" was executed 0 times
    And the Temporal test server holds no run history for that start

  @security @REQ-RT-032 @id:RT-032-01
  Scenario: A readable step payload in history stops the run
    Given step "fetch" recorded an unencrypted payload in the run history of a run on the temporal runtime
    When the run reads the result of step "fetch"
    Then step "fetch" fails with code "invalid_argument"
    And the run is "failed"

  @security @REQ-RT-033 @id:RT-033-01
  Scenario: A claim-checked payload of another tenant cannot be read by the run
    Given the payload codec claim-check threshold is 5 KB
    And tenant "globex" has stored a blob of 8192 bytes of seed 11
    And a run on the temporal runtime for tenant "acme" has a step "fetch" result in its run history naming that blob's content key
    When the run reads the result of step "fetch"
    Then step "fetch" fails with code "not_found"
    And the run is "failed"

  @REQ-RT-034 @id:RT-034-01
  Scenario: A checkpoint continues the run as new once the history reaches the threshold
    Given the checkpoint event threshold is 20
    And a flow calls 40 steps "s1" to "s40" and checkpoints after each, then returns "done"
    When a run of "steps" on the temporal runtime completes
    Then the run was continued as new at least once
    And the run is "completed"

  @REQ-RT-035 @id:RT-035-01
  Scenario: A checkpoint below the threshold keeps the run in its history
    Given the checkpoint event threshold is 1000
    And a flow calls 3 steps "s1" to "s3", checkpoints, then waits at gate "approve"
    And a run of "short" on the temporal runtime has started and waits at gate "approve"
    When gate "approve" is resolved with payload {"approved": true}
    Then the run was continued as new 0 times
    And the run returns {"approved": true}

  @REQ-RT-035 @id:RT-035-02
  Scenario: A checkpoint far below the threshold keeps the run in its history
    Given the checkpoint event threshold is 1000
    And a flow calls 6 steps "s1" to "s6", checkpoints, then returns "done"
    When a run of "steps" on the temporal runtime completes
    Then the run was continued as new 0 times
    And the run returns "done"

  @REQ-RT-036 @id:RT-036-01
  Scenario: A run continued as new keeps its id, results and awaited gate
    Given the checkpoint event threshold is 20
    And a flow calls 40 steps "s1" to "s40", checkpoints, then waits at gate "approve" and returns {"done": true}
    And run "r1" of that flow on the temporal runtime is waiting at gate "approve" after continuing as new
    When gate "approve" on run "r1" is resolved with payload {"approved": true}
    Then steps "s1" to "s40" were executed once each
    And run "r1" is "completed"
    And the run returns {"done": true}

  @REQ-RT-036 @id:RT-036-02
  Scenario: Emitted events before a continue-as-new reach the run's outbox once, in order
    Given the checkpoint event threshold is 20
    And a flow emits "a", "b", "c", "d", "e" and "f", calls 40 steps "s1" to "s40", checkpoints, calls 5 steps "t1" to "t5", then returns "done"
    When a run of "emit" on the temporal runtime completes
    Then the run was continued as new 1 time
    And the run's durable events are "a", "b", "c", "d", "e", "f"
    And the run returns "done"

  @REQ-RT-037 @id:RT-037-01
  Scenario: The temporal runtime starts against a server at the pinned version
    When the temporal runtime starts against the Temporal test server
    Then the runtime is serving

  @REQ-RT-037 @id:RT-037-02
  Scenario Outline: The temporal runtime refuses a server outside the pinned range with unsupported
    Given a simulated Temporal server version "<server>"
    When the temporal runtime starts against that server
    Then starting the runtime fails with code "unsupported"

    Examples:
      | server |
      | 1.31.9 |
      | 1.33.0 |

  @REQ-RT-038 @id:RT-038-01
  Scenario: A checkpoint waits for an in-flight step before continuing as new
    Given the checkpoint event threshold is 20
    And a flow calls 5 steps "s1" to "s5", starts steps "a" and "b" in parallel, calls checkpoint while "b" is in flight, then returns "done"
    When a run of "parallel" on the temporal runtime completes
    Then steps "s1" to "s5", "a" and "b" were executed once each
    And step "b" completed before the continue-as-new
    And the run was continued as new 1 time
    And the run returns "done"
