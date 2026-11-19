@wp:WP-L1-06 @level:component
Feature: Credential references and tenancy
  A caller holds a `cred://<name>` reference, never a value. A reference is
  resolved only for the tenant that stored it: another tenant's credential is
  invisible and a missing one looks the same. A value may be replaced while a
  run or a resolution is in flight.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a credential store over an empty SQL store with master key "mk-1"

  @REQ-CRED-008 @id:CRED-008-01
  Scenario Outline: A reference of the form cred:// followed by a valid name resolves
    Given the value "sk-live-4f9a2c" is put as "<name>" for tenant "acme"
    When the reference "<ref>" is resolved for tenant "acme"
    Then the resolution returns "sk-live-4f9a2c"

    Examples:
      | name           | ref                   |
      | a              | cred://a              |
      | Vendor.A_1/x-y | cred://Vendor.A_1/x-y |

  @security @REQ-CRED-009 @id:CRED-009-01
  Scenario Outline: A malformed reference is rejected
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the reference "<ref>" is resolved for tenant "acme"
    Then the resolution fails with code "invalid_argument"
    And the error does not contain "<ref>"
    And no value is returned

    Examples:
      | ref |
      | vendor/anthropic |
      | vault://vendor/anthropic |
      | cred://vendor/anthropic% |
      | cred://vendor/anthropic?version=1 |
      | cred://Vendor anthropic |
      | cred://. |
      | cred://a/./b |
      | cred://../a |
      | cred://a/../b |
      | cred:///a |
      | cred://a/ |
      | cred://a//b |

  @security @REQ-CRED-009 @id:CRED-009-02
  Scenario: A reference with no name is rejected
    When the reference "cred://" is resolved for tenant "acme"
    Then the resolution fails with code "invalid_argument"
    And no value is returned

  @security @REQ-CRED-010 @id:CRED-010-01
  Scenario: A reference to a missing credential fails
    When the reference "cred://vendor/absent" is resolved for tenant "acme"
    Then the resolution fails with code "not_found"
    And no value is returned

  @security @REQ-CRED-011 @id:CRED-011-01
  Scenario: Another tenant's credential does not resolve
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the reference "cred://vendor/anthropic" is resolved for tenant "globex"
    Then the resolution fails with code "not_found"
    And no value is returned

  @security @REQ-CRED-012 @id:CRED-012-01
  Scenario: A listing shows only the calling tenant's credentials
    Given the value "sk-a" is put as "vendor/a" for tenant "acme"
    And the value "sk-b" is put as "vendor/b" for tenant "acme"
    And the value "sk-g" is put as "vendor/g" for tenant "globex"
    When tenant "acme" lists its credentials
    Then the listing of tenant "acme" names:
      | name     |
      | vendor/a |
      | vendor/b |

  @security @REQ-CRED-013 @id:CRED-013-01
  Scenario: Two tenants can use the same credential name
    Given the value "sk-acme" is put as "vendor/x" for tenant "acme"
    And the value "sk-globex" is put as "vendor/x" for tenant "globex"
    When the reference "cred://vendor/x" is resolved for tenant "acme"
    And the reference "cred://vendor/x" is resolved for tenant "globex"
    Then the resolution for tenant "acme" returns "sk-acme"
    And the resolution for tenant "globex" returns "sk-globex"

  @security @REQ-CRED-024 @id:CRED-024-01
  Scenario: A run in flight resolves the new value after a replacement
    Given the value "sk-live-old" is put as "vendor/anthropic" for tenant "acme"
    And principal "human:ana" in run "r1" at step "s1" resolves "cred://vendor/anthropic" for tenant "acme" to "sk-live-old"
    When the value "sk-live-new" is put as "vendor/anthropic" for tenant "acme"
    And principal "human:ana" in run "r1" at step "s2" resolves "cred://vendor/anthropic" for tenant "acme"
    Then the resolution returns "sk-live-new"

  @security @REQ-CRED-025 @id:CRED-025-01
  Scenario: A resolution in flight keeps the value it read
    Given the value "sk-live-old" is put as "vendor/anthropic" for tenant "acme"
    And a resolution of "cred://vendor/anthropic" for tenant "acme" by principal "human:ana" in run "r1" at step "s1" is waiting for the secrets port
    When the value "sk-live-new" is put as "vendor/anthropic" for tenant "acme"
    And the waiting resolution is released
    And the reference "cred://vendor/anthropic" is resolved for tenant "acme"
    Then the waiting resolution returns "sk-live-old"
    And the resolution returns "sk-live-new"
