@planned @wp:WP-L1-06 @level:component
Feature: Credential storage at rest
  A value is stored only as ciphertext produced by the Secrets port. The
  store keeps no plaintext, survives a restart, and refuses a stored row it
  cannot decrypt.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a credential store over an empty SQL store with master key "mk-1"

  @security @REQ-CRED-021 @id:CRED-021-01
  Scenario: A stored credential is ciphertext only
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    Then the stored credential of tenant "acme" for "vendor/anthropic" contains no plaintext of "sk-live-4f9a2c"
    And the stored ciphertext of tenant "acme" for "vendor/anthropic" decrypts through the secrets port to "sk-live-4f9a2c"
    And the credential store holds exactly 1 stored credential for tenant "acme"

  @REQ-CRED-022 @id:CRED-022-01
  Scenario: Credentials survive a restart
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the credential store reopens on the same SQL store and master key "mk-1"
    And the reference "cred://vendor/anthropic" is resolved for tenant "acme"
    Then the resolution returns "sk-live-4f9a2c"
    And the listing of tenant "acme" names "vendor/anthropic"

  @security @REQ-CRED-023 @REQ-CRED-018 @id:CRED-023-01
  Scenario: A stored credential the secrets port rejects fails the resolution
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    And the stored ciphertext of tenant "acme" for "vendor/anthropic" is altered
    When the reference "cred://vendor/anthropic" is resolved for tenant "acme" by principal "human:ana" in run "r1" at step "s2"
    Then the resolution fails with code "internal"
    And no value is returned
    And the audit log of tenant "acme" is exactly:
      | principal | credential       | run | step | outcome  | at                   |
      | human:ana | vendor/anthropic | r1  | s2   | internal | 2026-10-01T09:00:00Z |
