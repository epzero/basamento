@wp:WP-L5-01 @runtime:local @level:component
Feature: The local runtime

  The local runtime runs flows in-process with an in-memory journal. It is not
  durable by design and says so; each scenario below checks one behavior of
  `runtime/local` (WP-L5-01). What every runtime shares is in Conformance
  (WP-L5-04); the Temporal runtime is WP-L5-02.

  @REQ-RT-005 @id:RT-005-01
  Scenario: The local runtime reports its capabilities honestly
    Given the runtime is "local"
    Then the runtime reports capability "durable" as false
    And the runtime reports capability "sync_gate_updates" as true
    And the runtime reports capability "continue_as_new" as false
    And the runtime reports capability "polyglot_queues" as false

  @REQ-RT-006 @id:RT-006-01
  Scenario: A new local runtime has no record of an earlier runtime's runs
    Given tenant "acme" has run "r1" of "review" on an earlier local runtime
    When a new local runtime starts and tenant "acme" describes run "r1"
    Then describing run "r1" fails with code "not_found"

  @REQ-RT-006 @id:RT-006-02
  Scenario: A new local runtime lists no runs of an earlier runtime
    Given tenant "acme" ran flow "review" on an earlier local runtime
    When a new local runtime starts and tenant "acme" lists runs of flow "review"
    Then tenant "acme" lists 0 runs

  @REQ-RT-007 @id:RT-007-01
  Scenario: A step runs once per step name in a run and its recorded result is reused
    Given step "fetch" returns {"call": 1}
    And a flow calls step "fetch" twice with the same name and returns both results
    When a run of "twice" completes
    Then step "fetch" was executed 1 time
    And both results are {"call": 1}

  @REQ-RT-007 @id:RT-007-02
  Scenario: Step results are recorded per run, not shared across runs
    Given step "fetch" returns {"call": 1} on execution 1 and {"call": 2} on execution 2
    And a flow calls step "fetch" once and returns its result
    When runs "r1" and "r2" of "once" complete in order on one local runtime
    Then step "fetch" was executed 2 times
    And run "r1" returns {"call": 1} and run "r2" returns {"call": 2}

  @REQ-RT-008 @id:RT-008-01
  Scenario: A timer does not complete before its duration has elapsed
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And a flow waits on a "30m" timer and then completes
    When a run of "timer" starts and 29 minutes pass
    Then the run is still "running"
    And the run has completed 0 timers

  @REQ-RT-008 @id:RT-008-02
  Scenario: A timer completes once the clock reaches its duration
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And a flow waits on a "30m" timer and then completes
    When a run of "timer" starts and 30 minutes pass
    Then the run is "completed"

  @REQ-RT-009 @id:RT-009-01
  Scenario: A resolved gate completes the run's gate wait
    Given a run waiting at gate "approve" that accepts payloads where "input.approved || size(input.notes) > 0"
    When the gate is resolved with payload {"approved": true}
    Then the run returns {"approved": true}
    And the run is "completed"

  @REQ-RT-010 @id:RT-010-01
  Scenario: Resolving a gate the run already received fails with conflict
    Given a flow waits at gate "approve" and then waits at gate "publish"
    And a run of "publish" resolved gate "approve" and received {"approved": true}
    When the gate "approve" is resolved again with payload {"notes": "second"}
    Then the resolution fails with code "conflict"
    And the run is "running" at gate "publish"

  @REQ-RT-011 @id:RT-011-01
  Scenario: Of a timer and an already resolved gate, the only ready operation is returned
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And a flow is waiting on the first of a "1h" timer and gate "approve", in that order
    And the gate is resolved with payload {"approved": true}
    When the run answers its pending wait on the first ready operation
    Then the wait returns index 1
    And the result at index 1 is {"approved": true}

  @REQ-RT-011 @id:RT-011-02
  Scenario: Waiting on the first of two ready operations returns the lower index
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And a flow is waiting on the first of a "2h" timer and a "1h" timer, in that order
    When 2 hours pass and the run answers its pending wait on the first ready operation
    Then the wait returns index 0

  @REQ-RT-011 @id:RT-011-03
  Scenario: Waiting on the first of two steps in flight returns the index of whichever finishes first
    Given a flow starts steps "a" and "b" and waits on the first to finish, in that order
    And step "a" finishes only after step "b"
    When the wait answers
    Then the wait returns index 1
    And step "b" was executed 1 time

  @REQ-RT-012 @id:RT-012-01
  Scenario: Cancelling a run stops the step it has in flight
    Given a flow runs step "render" that blocks until cancelled
    And run "c1" of "render" has step "render" in flight
    When run "c1" is cancelled with reason "user"
    Then the run is "cancelled"
    And step "render" received cancellation before the run closed

  @REQ-RT-013 @id:RT-013-01
  Scenario Outline: A pending wait ends with "cancelled" when its run is cancelled
    Given a run of "<flow>" is waiting on <wait>
    When the run is cancelled
    Then the run is "cancelled"
    And the pending wait fails with code "cancelled"

    Examples:
      | flow    | wait           |
      | waiting | gate "approve" |
      | timer   | a "1h" timer   |

  @REQ-RT-014 @id:RT-014-01
  Scenario: A child flow runs as its own run under the child run id the parent chose
    Given tenant "acme" has parent flow "parent" that starts child flow "child" with child run id "child-1"
    When a run of "parent" for tenant "acme" completes
    Then tenant "acme" describes run "child-1" as "completed"
    And tenant "globex" describing run "child-1" fails with code "not_found"

  @REQ-RT-014 @id:RT-014-02
  Scenario: A child run returns its output to the parent run
    Given parent flow "parent" starts child flow "child" with child run id "child-1"
    And flow "child" returns {"status": "ok"}
    When a run of "parent" completes
    Then the parent receives child output {"status": "ok"}

  @REQ-RT-015 @id:RT-015-01
  Scenario: Under close policy "terminate" the parent's end terminates an in-flight child
    Given parent flow "parent" starts child flow "child" with child run id "child-1" and close policy "terminate"
    And flow "child" waits at gate "approve"
    When a run of "parent" completes without waiting for "child-1"
    Then run "child-1" is "terminated"

  @REQ-RT-015 @id:RT-015-02
  Scenario: Under close policy "abandon" the parent's end leaves an in-flight child running
    Given parent flow "parent" starts child flow "child" with child run id "child-1" and close policy "abandon"
    And flow "child" waits at gate "approve"
    When a run of "parent" completes without waiting for "child-1"
    Then run "child-1" is "running"

  @REQ-RT-015 @id:RT-015-03
  Scenario: Under close policy "request_cancel" the parent's end cancels an in-flight child
    Given parent flow "parent" starts child flow "child" with child run id "child-1" and close policy "request_cancel"
    And flow "child" waits at gate "approve"
    When a run of "parent" completes without waiting for "child-1"
    Then run "child-1" is "cancelled"

  @REQ-RT-016 @id:RT-016-01
  Scenario: A step that fails with a non-retryable error stops after 1 attempt
    Given step "charge" fails with code "invalid_argument" marked retryable false
    When a run of "pay" runs
    Then step "charge" was executed 1 time
    And the step fails with code "invalid_argument"
    And the run is "failed"

  @REQ-RT-016 @id:RT-016-02
  Scenario: An error code listed as non-retryable stops retries even when marked retryable
    Given step "charge" fails with code "rate_limited" marked retryable true
    And the step lists "rate_limited" as non-retryable
    When a run of "pay" runs
    Then step "charge" was executed 1 time
    And the step fails with code "rate_limited"
    And the run is "failed"

  @REQ-RT-017 @id:RT-017-01
  Scenario: A retryable step that never succeeds fails after its configured attempt limit
    Given the clock is frozen at "2026-11-17T09:00:00Z"
    And step "charge" fails with code "rate_limited" on execution 1 and with code "vendor_error" on execution 2, each marked retryable true
    And the step allows 2 attempts
    When a run of "pay" runs
    Then step "charge" was executed 2 times
    And the step fails with the error of its last attempt with code "vendor_error"
    And no retry waited for the clock to advance
    And the run is "failed"

  @REQ-RT-027 @id:RT-027-01
  Scenario: A retryable step that succeeds before its attempt limit stops retrying
    Given step "charge" fails with code "vendor_error" marked retryable true on execution 1 and returns {"charged": true} on execution 2
    And the step allows 3 attempts
    When a run of "pay" runs
    Then step "charge" was executed 2 times
    And the run returns {"charged": true}
    And the run is "completed"

  @REQ-RT-018 @id:RT-018-01
  Scenario: A step with attempt limit 0 is tried 3 times by default
    Given step "charge" fails with code "vendor_error" marked retryable true
    And the step leaves its attempt limit at 0
    When a run of "pay" runs
    Then step "charge" was executed 3 times
    And the step fails with code "vendor_error"
    And the run is "failed"

  @REQ-RT-019 @id:RT-019-01
  Scenario: Independent steps execute concurrently
    Given a flow starts steps "a", "b" and "c" and waits on all three
    And each of steps "a", "b" and "c" waits until all 3 steps are executing
    When a run of "fanout" completes
    Then 3 steps executed concurrently
    And the run is "completed"

  @REQ-RT-021 @security @id:RT-021-01
  Scenario: Two tenants' runs of the same run id do not share state or step results
    Given step "fetch" returns {"tenant": "acme"} for tenant "acme" and {"tenant": "globex"} for tenant "globex"
    And tenant "acme" runs flow "work" as run "shared-1" that calls step "fetch"
    And tenant "globex" runs flow "work" as run "shared-1" that calls step "fetch"
    When both runs complete on one local runtime
    Then step "fetch" was executed 2 times
    And run "shared-1" of tenant "acme" returns {"tenant": "acme"}
    And run "shared-1" of tenant "globex" returns {"tenant": "globex"}

  @REQ-RT-022 @id:RT-022-01
  Scenario: Shutting down the local runtime leaves in-flight runs in state running
    Given a run of "timer" is in flight on a running local runtime
    When the local runtime shuts down
    Then shutdown completes with no error
    And the run is described as "running"

  @REQ-RT-023 @id:RT-023-01
  Scenario: Shutting down the local runtime cancels in-flight steps and waits for them to stop
    Given a run of "render" has step "render" in flight on a running local runtime
    When the local runtime shuts down
    Then shutdown completes with no error
    And step "render" received cancellation
    And 0 steps are executing

  @REQ-RT-024 @id:RT-024-01
  Scenario: A local runtime that has shut down rejects new runs with unavailable
    Given a running local runtime has shut down
    When a run of "linear" is started on that runtime
    Then starting the run fails with code "unavailable"

  @REQ-RT-025 @security @id:RT-025-01
  Scenario: Describing another tenant's run fails with not_found
    Given tenant "acme" has run "shared-1" waiting at gate "approve"
    When tenant "globex" describes run "shared-1"
    Then describing run "shared-1" fails with code "not_found"

  @REQ-RT-025 @security @id:RT-025-02
  Scenario: Cancelling another tenant's run fails with not_found
    Given tenant "acme" has run "shared-1" waiting at gate "approve"
    When tenant "globex" cancels run "shared-1"
    Then cancelling run "shared-1" fails with code "not_found"
    And tenant "acme" describes run "shared-1" as "running"

  @REQ-RT-025 @security @id:RT-025-03
  Scenario: Resolving a gate of another tenant's run fails with not_found
    Given tenant "acme" has run "shared-1" waiting at gate "approve"
    When tenant "globex" resolves gate "approve" of run "shared-1" with payload {"approved": true}
    Then resolving the gate fails with code "not_found"
    And tenant "acme" describes run "shared-1" as "running"

  @REQ-RT-026 @security @id:RT-026-01
  Scenario: Listing runs returns only the caller's tenant's runs
    Given tenant "acme" has run "shared-1" of flow "review" waiting at gate "approve"
    And tenant "globex" has run "shared-1" of flow "review" waiting at gate "approve"
    When tenant "globex" lists runs of flow "review"
    Then tenant "globex" lists 1 run for tenant "globex" with run id "shared-1"

  # The tenant comes from the context (`core.TenantFrom`); a tenant field the
  # call carries may be empty or equal to it, never a different one. Each call
  # below is made with context tenant "acme" and asks for tenant "globex".

  @wp:WP-L5-01 @security @REQ-RT-042 @id:RT-042-01
  Scenario: Starting a run for another tenant than the context's is rejected with invalid_argument
    Given a flow calls step "render" that returns {"ok": true}
    When run "r1" of "render" is started with context tenant "acme" and request tenant "globex"
    Then starting the run fails with code "invalid_argument"
    And step "render" was executed 0 times
    And tenant "acme" has no run with id "r1"
    And tenant "globex" has no run with id "r1"

  @wp:WP-L5-01 @security @REQ-RT-042 @id:RT-042-02
  Scenario: Describing a run for another tenant than the context's is rejected with invalid_argument
    Given tenant "globex" has run "shared-1" waiting at gate "approve"
    And the request names a run reference of tenant "globex" to run "shared-1"
    When run "shared-1" is described with context tenant "acme"
    Then describing run "shared-1" fails with code "invalid_argument"

  @wp:WP-L5-01 @security @REQ-RT-042 @id:RT-042-03
  Scenario: Cancelling a run for another tenant than the context's is rejected with invalid_argument
    Given tenant "globex" has run "shared-1" waiting at gate "approve"
    And the request names a run reference of tenant "globex" to run "shared-1"
    When run "shared-1" is cancelled with context tenant "acme"
    Then cancelling run "shared-1" fails with code "invalid_argument"
    And tenant "globex" describes run "shared-1" as "running"

  @wp:WP-L5-01 @security @REQ-RT-042 @id:RT-042-04
  Scenario: Resolving a gate for another tenant than the context's is rejected with invalid_argument
    Given tenant "globex" has run "shared-1" waiting at gate "approve"
    And the request names a run reference of tenant "globex" to run "shared-1"
    When gate "approve" of run "shared-1" is resolved with context tenant "acme" and payload {"approved": true}
    Then resolving the gate fails with code "invalid_argument"
    And tenant "globex" describes run "shared-1" as "running"

  @wp:WP-L5-01 @security @REQ-RT-042 @id:RT-042-05
  Scenario: Listing runs for another tenant than the context's is rejected with invalid_argument
    Given tenant "globex" has run "shared-1" of flow "review" waiting at gate "approve"
    When runs of flow "review" are listed for request tenant "globex" with context tenant "acme"
    Then listing runs fails with code "invalid_argument"

  @wp:WP-L5-01 @security @REQ-RT-042 @id:RT-042-06
  Scenario Outline: Starting a run with an empty or matching request tenant uses the context tenant
    Given flow "review" waits at gate "approve"
    When run "r1" of "review" is started with context tenant "acme" and request tenant "<tenant>"
    Then tenant "acme" describes run "r1" as "running"

    Examples:
      | tenant |
      |        |
      | acme   |

  @wp:WP-L5-01 @security @REQ-RT-043 @id:RT-043-01
  Scenario: The run reference a start returns carries the run's tenant
    Given a flow calls step "render" that returns {"ok": true}
    When run "r1" of "render" is started with context tenant "acme" and an empty request tenant
    Then the run reference the start returned is run "r1" of tenant "acme"

  @wp:WP-L5-01 @security @REQ-RT-043 @id:RT-043-02
  Scenario: Describing a run returns a run reference carrying that run's tenant
    Given tenant "globex" has run "shared-1" waiting at gate "approve"
    When run "shared-1" is described with context tenant "globex" and an empty reference tenant
    Then the run reference the description returned is run "shared-1" of tenant "globex"

  @wp:WP-L5-01 @security @REQ-RT-043 @id:RT-043-03
  Scenario: Listing runs returns run references carrying each run's tenant
    Given tenant "globex" has run "shared-1" of flow "review" waiting at gate "approve"
    When runs of flow "review" are listed with context tenant "globex" and an empty query tenant
    Then tenant "globex" lists 1 run for tenant "globex" with run id "shared-1"

  @wp:WP-L5-01 @security @REQ-RT-044 @id:RT-044-01
  Scenario: Starting a run without a tenant in the context is rejected with invalid_argument
    Given a flow calls step "render" that returns {"ok": true}
    When a run of "render" is started without a context tenant and with an empty request tenant
    Then starting the run fails with code "invalid_argument"
    And step "render" was executed 0 times

  @wp:WP-L5-01 @security @REQ-RT-044 @id:RT-044-02
  Scenario: Starting a run without a tenant in the context is rejected even when the request names one
    Given a flow calls step "render" that returns {"ok": true}
    When run "r1" of "render" is started without a context tenant and with request tenant "acme"
    Then starting the run fails with code "invalid_argument"
    And step "render" was executed 0 times
    And tenant "acme" has no run with id "r1"

  @wp:WP-L5-01 @security @REQ-RT-044 @id:RT-044-03
  Scenario Outline: Describing a run without a tenant in the context is rejected with invalid_argument
    Given tenant "acme" has run "shared-1" of flow "review" waiting at gate "approve"
    When run "shared-1" is described without a context tenant and with reference tenant "<tenant>"
    Then describing run "shared-1" fails with code "invalid_argument"

    Examples:
      | tenant |
      |        |
      | acme   |

  @wp:WP-L5-01 @security @REQ-RT-044 @id:RT-044-04
  Scenario Outline: Cancelling a run without a tenant in the context is rejected with invalid_argument
    Given tenant "acme" has run "shared-1" of flow "review" waiting at gate "approve"
    When run "shared-1" is cancelled without a context tenant and with reference tenant "<tenant>"
    Then cancelling run "shared-1" fails with code "invalid_argument"
    And tenant "acme" describes run "shared-1" as "running"

    Examples:
      | tenant |
      |        |
      | acme   |

  @wp:WP-L5-01 @security @REQ-RT-044 @id:RT-044-05
  Scenario Outline: Resolving a gate without a tenant in the context is rejected with invalid_argument
    Given tenant "acme" has run "shared-1" of flow "review" waiting at gate "approve"
    When gate "approve" of run "shared-1" is resolved without a context tenant, reference tenant "<tenant>" and payload {"approved": true}
    Then resolving the gate fails with code "invalid_argument"
    And tenant "acme" describes run "shared-1" as "running"

    Examples:
      | tenant |
      |        |
      | acme   |

  @wp:WP-L5-01 @security @REQ-RT-044 @id:RT-044-06
  Scenario Outline: Listing runs without a tenant in the context is rejected with invalid_argument
    Given tenant "acme" has run "shared-1" of flow "review" waiting at gate "approve"
    When runs of flow "review" are listed without a context tenant and with query tenant "<tenant>"
    Then listing runs fails with code "invalid_argument"

    Examples:
      | tenant |
      |        |
      | acme   |
