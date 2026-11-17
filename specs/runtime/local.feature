@planned @wp:WP-L5-01 @runtime:local @level:component
Feature: The local runtime

  The local runtime runs flows in-process with an in-memory journal. It is not
  durable by design and says so; each scenario below touches one behavior that
  is particular to it (WP-L5-01). What every runtime shares is in
  Conformance (WP-L5-04); the Temporal runtime is WP-L5-02.

  @REQ-RT-005 @id:RT-005-01
  Scenario: The local runtime does not advertise durability
    Given the runtime is "local"
    Then the runtime capabilities have "Durable" false
    And the runtime capabilities have "SyncGateUpdates" true
    And the runtime capabilities have "ContinueAsNew" false
    And the runtime capabilities have "PolyglotQueues" false

  @REQ-RT-006 @id:RT-006-01
  Scenario: A new process's runtime has no record of an earlier process's runs
    Given tenant "acme" has run "r1" of "review" on an earlier local runtime
    When a new local runtime starts and tenant "acme" describes run "r1"
    Then run "r1" is described as code "not_found"

  @REQ-RT-006 @id:RT-006-02
  Scenario: A new process's runtime lists no runs of an earlier process
    Given tenant "acme" ran flow "review" on an earlier local runtime
    When a new local runtime starts and tenant "acme" lists runs of flow "review"
    Then tenant "acme" lists 0 runs

  @REQ-RT-007 @id:RT-007-01
  Scenario: A step runs once per run and its result is reused
    Given a flow calls step "fetch" twice and returns both results
    When a run of "twice" completes
    Then step "fetch" was executed 1 time
    And both results are {"lines": 3}

  @REQ-RT-007 @id:RT-007-02
  Scenario: Step results are kept per run, not per runtime
    Given a flow calls step "fetch" once and returns its result
    When runs "r1" and "r2" of "once" complete on one local runtime
    Then step "fetch" was executed 2 times
    And both runs return a result

  @REQ-RT-008 @id:RT-008-01
  Scenario: A timer does not complete before its time
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And a flow calls "Sleep" for "30m" and then completes
    When a run of "timer" starts and 29 minutes pass
    Then the run is still "running"
    And the run completed exactly 0 calls of "Sleep"

  @REQ-RT-008 @id:RT-008-02
  Scenario: A timer completes once the clock reaches its time
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And a flow calls "Sleep" for "30m" and then completes
    When a run of "timer" starts and 30 minutes pass
    Then the run is "completed"

  @REQ-RT-009 @id:RT-009-01
  Scenario: A resolved gate completes the run's wait
    Given a run waiting at gate "approve" that accepts payloads where "input.approved || size(input.notes) > 0"
    When the gate is resolved with payload {"approved": true}
    Then the run returns {"gate": {"approved": true}}
    And the run is "completed"

  @REQ-RT-010 @id:RT-010-01
  Scenario: Resolving a gate the run already received is a conflict
    Given a run that resolved gate "approve" and received {"approved": true}
    When the gate is resolved again with payload {"notes": "second"}
    Then the resolution fails with code "conflict"
    And the run is "completed"

  @REQ-RT-011 @id:RT-011-01
  Scenario: Select answers with the gate's index when it is already ready
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And a flow waits on a 1-hour timer and gate "approve", in that order
    And the gate is resolved with payload {"approved": true}
    When the flow reaches Select and 1 hour passes
    Then Select returns index 1
    And the result of index 1 is {"approved": true}

  @REQ-RT-011 @id:RT-011-02
  Scenario: Select answers with the lower index when two futures are ready
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And a flow waits on a 1-hour timer and a 2-hour timer, in that order
    When the flow reaches Select and 2 hours pass
    Then Select returns index 0

  @REQ-RT-012 @id:RT-012-01
  Scenario: Cancelling a run stops the step it is running
    Given a flow runs step "render" that runs until it is cancelled
    And a run of "render" has step "render" in flight
    When run "c1" is cancelled with reason "user"
    Then the run is "cancelled"
    And step "render" saw its context cancelled before the run closed

  @REQ-RT-013 @id:RT-013-01
  Scenario Outline: A pending wait ends with "cancelled" when its run is cancelled
    Given a run of "<flow>" is waiting on <wait>
    When the run is cancelled
    Then the run is "cancelled"
    And the pending call completes with code "cancelled"

    Examples:
      | flow    | wait             |
      | waiting | gate "approve"   |
      | timer   | "Sleep" for "1h" |

  @REQ-RT-014 @id:RT-014-01
  Scenario: A child flow runs as its own run under the id the parent chose
    Given a parent flow "parent" calls "Child" for flow "child" with child run id "child-1"
    When a run of "parent" completes
    Then run "child-1" is "completed"

  @REQ-RT-014 @id:RT-014-02
  Scenario: A child returns its output to the parent
    Given a parent flow "parent" calls "Child" for flow "child" with child run id "child-1"
    And flow "child" returns {"status": "ok"}
    When a run of "parent" completes
    Then the parent's child future yields {"status": "ok"}

  @REQ-RT-015 @id:RT-015-01
  Scenario: A parent that ends terminates the children it left in flight
    Given flow "parent" calls "Child" for flow "child" with child run id "child-1" and policy "terminate"
    And flow "child" waits at gate "approve"
    When a run of "parent" completes
    Then run "child-1" is "terminated"

  @REQ-RT-015 @id:RT-015-02
  Scenario: Under policy "abandon" the parent's end leaves the child running
    Given flow "parent" calls "Child" for flow "child" with child run id "child-1" and policy "abandon"
    And flow "child" waits at gate "approve"
    When a run of "parent" completes
    Then run "child-1" is "running"

  @REQ-RT-015 @id:RT-015-03
  Scenario: Under policy "request_cancel" the parent's end cancels the child
    Given flow "parent" calls "Child" for flow "child" with child run id "child-1" and policy "request_cancel"
    And flow "child" waits at gate "approve"
    When a run of "parent" completes
    Then run "child-1" is "cancelled"

  @REQ-RT-016 @id:RT-016-01
  Scenario: A step that reports retryable false fails the run at once
    Given step "charge" fails in a way that reports retryable false
    When a run of "pay" starts
    Then step "charge" was executed 1 time
    And the run is "failed"

  @REQ-RT-016 @id:RT-016-02
  Scenario: A code listed in NonRetryable stops the retries although it is retryable
    Given step "charge" fails with code "rate_limited", which reports retryable true
    And the step's options list "rate_limited" as non-retryable
    When a run of "pay" starts
    Then step "charge" was executed 1 time
    And the run is "failed"

  @REQ-RT-017 @id:RT-017-01
  Scenario: A step that never succeeds fails the run after its attempt limit
    Given step "charge" fails in a way that reports retryable true
    And the step's options allow 3 attempts
    When a run of "pay" starts
    Then step "charge" was executed 3 times
    And the run fails with code "unavailable"

  @REQ-RT-018 @id:RT-018-01
  Scenario: A step that leaves MaxAttempts at 0 is tried 3 times
    Given step "charge" fails in a way that reports retryable true
    And the step's options leave the attempt limit at 0
    When a run of "pay" starts
    Then step "charge" was executed 3 times
    And the run fails with code "unavailable"

  @REQ-RT-019 @id:RT-019-01
  Scenario: Independent steps run at the same time
    Given a flow starts steps "a", "b" and "c" and waits on them together
    And each of steps "a", "b" and "c" waits until all three of them are executing
    When a run of "fanout" starts
    Then 3 steps are executing at once
    And the run is "completed"

  @REQ-RT-020 @id:RT-020-01
  Scenario: Select reports the ready step's own index when the steps finish out of order
    Given a run's Select waits on steps "a" and "b" in that order
    And step "a" finishes only after step "b"
    When the run's Select answers
    Then Select returns index 0
    And step "a" was executed 1 time

  @REQ-RT-021 @id:RT-021-01
  Scenario: Two tenants' runs do not share step results
    Given tenant "acme" runs flow "work" that calls step "fetch"
    And tenant "globex" runs flow "work" that calls step "fetch"
    When both runs are in flight on one local runtime and both complete
    Then step "fetch" was executed 2 times
    And both runs return their own result

  @REQ-RT-021 @id:RT-021-02
  Scenario: A run id of one tenant does not reach another tenant's run
    Given tenant "acme" has run "shared-1" that waits at gate "approve"
    When tenant "globex" describes run "shared-1"
    Then run "shared-1" is described as code "not_found"

  @REQ-RT-021 @id:RT-021-03
  Scenario: A tenant cannot cancel another tenant's run
    Given tenant "acme" has run "shared-1" that waits at gate "approve"
    When tenant "globex" cancels run "shared-1"
    Then the cancellation fails with code "not_found"
    And tenant "acme" describes run "shared-1" as "running"

  @REQ-RT-022 @id:RT-022-01
  Scenario: Shutdown leaves in-flight runs running
    Given a run of "timer" is in flight on a served local runtime
    When the Serve context is cancelled
    Then Serve returns no error
    And the run is described as "running"

  @REQ-RT-022 @id:RT-022-02
  Scenario: Shutdown stops the steps in flight before Serve returns
    Given a run of "render" has step "render" in flight on a served local runtime
    When the Serve context is cancelled
    Then Serve returns no error
    And step "render" saw its context cancelled
    And no steps are executing

  @REQ-RT-022 @id:RT-022-03
  Scenario: A served runtime takes no new runs after shutdown
    Given a run of "timer" is in flight on a served local runtime
    And the Serve context is cancelled
    When a run of "linear" is started
    Then the start fails with code "unavailable"
