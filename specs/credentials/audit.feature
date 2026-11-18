@planned @wp:WP-L1-06 @level:component
Feature: Credential audit
  Every resolution of a well-formed reference for a tenant, successful or not,
  records one `platform.AuditEvent` through the `platform.Audit` port naming who
  used which credential in which run and step. The audit event, the error and
  the log never carry the value. A resolution whose audit event cannot be
  written fails closed.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a credential store over an empty SQL store with master key "mk-1"

  @security @REQ-CRED-017 @id:CRED-017-01
  Scenario: A successful resolution is audited
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the reference "cred://vendor/anthropic" is resolved for tenant "acme" by principal "human:ana" in run "r1" at step "s2"
    Then the resolution returns "sk-live-4f9a2c"
    And the audit log of tenant "acme" is exactly:
      | principal | action             | resource                | run | step | outcome | reason | time                 |
      | human:ana | credential.resolve | cred://vendor/anthropic | r1  | s2   | allowed |        | 2026-10-01T09:00:00Z |

  @security @REQ-CRED-017 @id:CRED-017-02
  Scenario: A resolution with no principal, run or step in context audits empty fields
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the reference "cred://vendor/anthropic" is resolved for tenant "acme"
    Then the resolution returns "sk-live-4f9a2c"
    And the audit log of tenant "acme" is exactly:
      | principal | action             | resource                | run | step | outcome | reason | time                 |
      |           | credential.resolve | cred://vendor/anthropic |     |      | allowed |        | 2026-10-01T09:00:00Z |

  @security @REQ-CRED-018 @id:CRED-018-01
  Scenario: A resolution that finds no credential is audited
    When the reference "cred://vendor/absent" is resolved for tenant "acme" by principal "human:ana" in run "r1" at step "s2"
    Then the resolution fails with code "not_found"
    And the audit log of tenant "acme" is exactly:
      | principal | action             | resource             | run | step | outcome | reason    | time                 |
      | human:ana | credential.resolve | cred://vendor/absent | r1  | s2   | failed  | not_found | 2026-10-01T09:00:00Z |

  @security @REQ-CRED-032 @REQ-CRED-009 @id:CRED-032-01
  Scenario: A malformed reference is rejected without an audit event
    When the reference "vault://vendor/anthropic" is resolved for tenant "acme" by principal "human:ana" in run "r1" at step "s2"
    Then the resolution fails with code "invalid_argument"
    And the audit port received 0 events
    And the audit log of tenant "acme" is empty

  @security @REQ-CRED-032 @REQ-CRED-005 @id:CRED-032-02
  Scenario: A resolution without a tenant records no audit event
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the credential store resolves "cred://vendor/anthropic" without a tenant
    Then the resolution fails with code "invalid_argument"
    And the audit port received 0 events
    And the audit log of tenant "acme" is empty

  @security @REQ-CRED-019 @id:CRED-019-01
  Scenario: No value appears in an error, the audit log or the log on a failed resolution
    Given the log is captured
    And the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    And the simulated key service is unreachable
    When the reference "cred://vendor/anthropic" is resolved for tenant "acme" by principal "human:ana" in run "r1" at step "s2"
    Then the resolution fails with code "unavailable"
    And neither the error nor the audit log of tenant "acme" nor the captured log contains "sk-live-4f9a2c"

  @security @REQ-CRED-019 @id:CRED-019-02
  Scenario: A successful resolution writes no value to the audit log or the log
    Given the log is captured
    And the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the reference "cred://vendor/anthropic" is resolved for tenant "acme" by principal "human:ana" in run "r1" at step "s2"
    Then the resolution returns "sk-live-4f9a2c"
    And neither the audit log of tenant "acme" nor the captured log contains "sk-live-4f9a2c"

  @security @REQ-CRED-019 @REQ-CRED-028 @id:CRED-019-03
  Scenario: A resolution whose audit write fails writes no value to the error or log
    Given the log is captured
    And the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    And the audit store rejects every record
    When the reference "cred://vendor/anthropic" is resolved for tenant "acme" by principal "human:ana" in run "r1" at step "s2"
    Then the resolution fails with code "unavailable"
    And neither the error nor the captured log contains "sk-live-4f9a2c"

  @security @REQ-CRED-028 @id:CRED-028-01
  Scenario: A resolution fails closed when its audit event cannot be written
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    And the audit store rejects every record
    When the reference "cred://vendor/anthropic" is resolved for tenant "acme" by principal "human:ana" in run "r1" at step "s2"
    Then the resolution fails with code "unavailable"
    And no value is returned
