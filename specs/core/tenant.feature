@planned @wp:WP-L0-01 @level:component
Feature: Tenant context
  A tenant id travels in the context as the isolation boundary for data and
  spend. The context reports no tenant when it carries none, or when the id it
  carries is not a valid tenant id.

  @REQ-CORE-005 @REQ-CORE-006 @security @id:CORE-006-01
  Scenario Outline: An invalid tenant id is reported as no tenant
    When a context carries <id>
    Then the context reports no tenant
    And the reported tenant id is empty

    Examples:
      | id                                      |
      | no tenant id                            |
      | an empty tenant id                      |
      | a tenant id of 129 bytes                |
      | a tenant id that is not valid UTF-8     |
      | a tenant id containing a NUL character  |
      | a tenant id containing U+001F           |
      | a tenant id containing U+007F           |
      | the tenant id "."                       |
      | the tenant id ".."                      |
      | the tenant id "a/b"                     |
      | the tenant id "a\b"                     |

  @REQ-CORE-005 @security @id:CORE-005-01
  Scenario: A short tenant id is reported unchanged
    When a context carries the tenant id "acme"
    Then the context reports the tenant id "acme"

  @REQ-CORE-005 @security @id:CORE-005-02
  Scenario: A tenant id of 128 bytes is reported unchanged
    Given a tenant id of 128 "a" characters
    When a context carries that tenant id
    Then the context reports that same tenant id

  @REQ-CORE-005 @security @id:CORE-005-03
  Scenario: A non-ASCII tenant id is reported unchanged
    When a context carries the tenant id "tènant-é"
    Then the context reports the tenant id "tènant-é"
