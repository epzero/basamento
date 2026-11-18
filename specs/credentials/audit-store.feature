@planned @wp:WP-L1-06 @level:component
Feature: Credential audit store
  The SQL implementation of `platform.Audit` appends every audit event to one
  table. A stored event is never updated or deleted; a tenant-scoped read
  returns only the calling tenant's events, never deployment-level ones.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a credential store over an empty SQL store with master key "mk-1"

  @security @REQ-CRED-029 @id:CRED-029-01
  Scenario: Recorded audit events survive a restart
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    And principal "human:ana" in run "r1" at step "s2" resolves "cred://vendor/anthropic" for tenant "acme" to "sk-live-4f9a2c"
    When the audit store reopens on the same SQL store
    Then the audit store of tenant "acme" holds exactly 1 event:
      | tenant | principal | action             | resource                | run | step | outcome | reason | time                 |
      | acme   | human:ana | credential.resolve | cred://vendor/anthropic | r1  | s2   | allowed |        | 2026-10-01T09:00:00Z |

  @security @REQ-CRED-030 @id:CRED-030-01
  Scenario: Audit events accumulate and are never replaced
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    And principal "human:ana" in run "r1" at step "s1" resolves "cred://vendor/anthropic" for tenant "acme" to "sk-live-4f9a2c"
    When principal "human:ana" in run "r1" at step "s2" resolves "cred://vendor/anthropic" for tenant "acme"
    Then the audit store of tenant "acme" holds exactly 2 events:
      | tenant | principal | action             | resource                | run | step | outcome | reason | time                 |
      | acme   | human:ana | credential.resolve | cred://vendor/anthropic | r1  | s1   | allowed |        | 2026-10-01T09:00:00Z |
      | acme   | human:ana | credential.resolve | cred://vendor/anthropic | r1  | s2   | allowed |        | 2026-10-01T09:00:00Z |

  @security @REQ-CRED-031 @id:CRED-031-01
  Scenario: Reading audit events is scoped to the calling tenant
    Given the value "sk-a" is put as "vendor/a" for tenant "acme"
    And the value "sk-g" is put as "vendor/g" for tenant "globex"
    And principal "human:ana" in run "r1" at step "s1" resolves "cred://vendor/a" for tenant "acme" to "sk-a"
    And principal "human:gina" in run "r2" at step "s1" resolves "cred://vendor/g" for tenant "globex" to "sk-g"
    When tenant "acme" reads its audit events
    Then the audit events read for tenant "acme" are exactly:
      | tenant | principal | action             | resource        | run | step | outcome | reason | time                 |
      | acme   | human:ana | credential.resolve | cred://vendor/a | r1  | s1   | allowed |        | 2026-10-01T09:00:00Z |

  @security @REQ-CRED-033 @id:CRED-033-01
  Scenario: Reading audit events without a tenant is rejected
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    And principal "human:ana" in run "r1" at step "s2" resolves "cred://vendor/anthropic" for tenant "acme" to "sk-live-4f9a2c"
    When audit events are read from the audit store without a tenant
    Then the audit store call fails with code "invalid_argument"

  @security @REQ-CRED-034 @id:CRED-034-01
  Scenario: Recording an audit event naming a tenant without a tenant in context is rejected
    When the audit store records a "credential.resolve" event with tenant "acme" and without a tenant in context
    Then the audit store call fails with code "invalid_argument"
    And the audit events read for tenant "acme" are empty

  @security @REQ-CRED-034 @id:CRED-034-02
  Scenario: Recording an audit event naming another tenant is rejected
    When the audit store records a "credential.resolve" event with tenant "acme" under context tenant "globex"
    Then the audit store call fails with code "invalid_argument"
    And the audit events read for tenant "acme" are empty
    And the audit events read for tenant "globex" are empty

  @security @REQ-CRED-035 @REQ-CRED-031 @id:CRED-035-01
  Scenario: A deployment-level audit event is stored and excluded from tenant reads
    When the audit store records a "worker.refuse" event for pool "acme-gpu" with an empty tenant and without a tenant in context
    Then the audit table holds 1 deployment-level event for action "worker.refuse" and resource "acme-gpu"
    And the audit events read for tenant "acme" are empty
