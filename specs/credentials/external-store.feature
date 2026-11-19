@wp:WP-L1-06 @level:component
Feature: External credential stores
  The store writes encrypted rows through a backing port. The SQL store is the
  embedded backend; an external secret manager implements the same port and
  sees only ciphertext. Nothing in the gate calls a vendor.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a credential store over an empty SQL store and a simulated external store with master key "mk-1"

  @security @REQ-CRED-026 @REQ-CRED-001 @id:CRED-026-01
  Scenario: A credential in an external store round-trips
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the reference "cred://vendor/anthropic" is resolved for tenant "acme"
    Then the resolution returns "sk-live-4f9a2c"
    And the simulated external store holds exactly 1 credential for tenant "acme"
    And the SQL store holds no credential for tenant "acme"

  @security @REQ-CRED-026 @REQ-CRED-021 @id:CRED-026-02
  Scenario: An external store holds only ciphertext
    When the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    Then the simulated external store holds no plaintext of "sk-live-4f9a2c"
    And the stored ciphertext in the simulated external store decrypts through the secrets port to "sk-live-4f9a2c"
