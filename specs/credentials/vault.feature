@planned @wp:WP-L1-06 @level:component
Feature: Credential values
  A tenant's vault holds named secrets. A value is put, replaced and deleted,
  and it is never read back: a caller holds a `cred://` reference and resolves
  it at the moment of use. Listings expose names and times only.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a credential store over an empty SQL store with master key "mk-1"

  @REQ-CRED-001 @REQ-CRED-008 @id:CRED-001-01
  Scenario: A value put under a name is returned by its reference
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the reference "cred://vendor/anthropic" is resolved for tenant "acme"
    Then the resolution returns "sk-live-4f9a2c"

  @REQ-CRED-002 @id:CRED-002-01
  Scenario: Putting a value again replaces the stored value
    Given the value "sk-live-old" is put as "vendor/anthropic" for tenant "acme"
    When the value "sk-live-new" is put as "vendor/anthropic" for tenant "acme"
    And the reference "cred://vendor/anthropic" is resolved for tenant "acme"
    Then the resolution returns "sk-live-new"

  @REQ-CRED-003 @id:CRED-003-01
  Scenario: A deleted credential no longer resolves
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the credential "vendor/anthropic" is deleted for tenant "acme"
    And the reference "cred://vendor/anthropic" is resolved for tenant "acme"
    Then the resolution fails with code "not_found"

  @REQ-CRED-004 @id:CRED-004-01
  Scenario: Deleting a credential that is not stored fails
    When the credential "vendor/absent" is deleted for tenant "acme"
    Then the deletion fails with code "not_found"

  @security @REQ-CRED-004 @id:CRED-004-02
  Scenario: One tenant cannot delete another tenant's credential
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the credential "vendor/anthropic" is deleted for tenant "globex"
    And the reference "cred://vendor/anthropic" is resolved for tenant "acme"
    Then the deletion fails with code "not_found"
    And the resolution returns "sk-live-4f9a2c"

  @security @REQ-CRED-005 @id:CRED-005-01
  Scenario: A put without a tenant is rejected
    When the credential store puts the value "sk-live-4f9a2c" under "vendor/anthropic" without a tenant
    Then the put fails with code "invalid_argument"
    And no credential is stored for any tenant

  @security @REQ-CRED-005 @id:CRED-005-02
  Scenario: A resolution without a tenant is rejected
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When the credential store resolves "cred://vendor/anthropic" without a tenant
    Then the resolution fails with code "invalid_argument"
    And no value is returned

  @REQ-CRED-006 @id:CRED-006-01
  Scenario Outline: A valid credential name is accepted
    Given the value "sk-live-4f9a2c" is put as "<name>" for tenant "acme"
    When the reference "cred://<name>" is resolved for tenant "acme"
    Then the resolution returns "sk-live-4f9a2c"

    Examples:
      | name |
      | a |
      | Vendor.A_1/x-y |
      | aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa |

  @REQ-CRED-007 @id:CRED-007-01
  Scenario Outline: An invalid credential name is rejected on put
    When the value "sk-live-4f9a2c" is put as "<name>" for tenant "acme"
    Then the put fails with code "invalid_argument"
    And no credential is stored for any tenant

    Examples:
      | name |
      | |
      | vendor anthropic |
      | vendor/anthropic% |
      | vendor:anthropic |
      | aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa |

  @REQ-CRED-007 @id:CRED-007-02
  Scenario: An invalid credential name is rejected on delete
    When the credential "vendor anthropic" is deleted for tenant "acme"
    Then the deletion fails with code "invalid_argument"

  @security @REQ-CRED-014 @id:CRED-014-01
  Scenario: A listing exposes names and no value
    Given the value "sk-live-4f9a2c" is put as "vendor/anthropic" for tenant "acme"
    When tenant "acme" lists its credentials
    Then the listing of tenant "acme" names "vendor/anthropic"
    And the listing of tenant "acme" contains no "sk-live-4f9a2c"

  @REQ-CRED-015 @id:CRED-015-01
  Scenario: A listing carries each credential's name and times
    Given the value "sk-a" is put as "vendor/a" for tenant "acme"
    And the clock advances by 5 minutes
    And the value "sk-b" is put as "vendor/b" for tenant "acme"
    When tenant "acme" lists its credentials
    Then the listing of tenant "acme" is exactly:
      | name     | created_at           | updated_at           |
      | vendor/a | 2026-10-01T09:00:00Z | 2026-10-01T09:00:00Z |
      | vendor/b | 2026-10-01T09:05:00Z | 2026-10-01T09:05:00Z |

  @REQ-CRED-015 @id:CRED-015-02
  Scenario: Replacing a value keeps the creation time and changes the last-updated time
    Given the value "sk-old" is put as "vendor/a" for tenant "acme"
    When the clock advances by 5 minutes
    And the value "sk-new" is put as "vendor/a" for tenant "acme"
    And tenant "acme" lists its credentials
    Then the listing of tenant "acme" is exactly:
      | name     | created_at           | updated_at           |
      | vendor/a | 2026-10-01T09:00:00Z | 2026-10-01T09:05:00Z |

  @REQ-CRED-016 @id:CRED-016-01
  Scenario: Listings are ordered by the bytes of the names
    Given the value "x" is put as "vendor/b" for tenant "acme"
    And the value "y" is put as "vendor/a" for tenant "acme"
    And the value "z" is put as "Vendor/c" for tenant "acme"
    When tenant "acme" lists its credentials
    Then the listing of tenant "acme" names:
      | name     |
      | Vendor/c |
      | vendor/a |
      | vendor/b |
