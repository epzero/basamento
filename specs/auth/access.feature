@security
Feature: Authentication and tenancy

  @planned @REQ-AUTH-001 @wp:WP-L8-01 @level:system @id:AUTH-001-01
  Scenario: The server refuses a public address without auth
    Given auth mode "none"
    When "kit serve" is asked to listen on "0.0.0.0:8080"
    Then the server does not start and explains that auth is required for non-loopback addresses

  @planned @REQ-AUTH-002 @wp:WP-L6-03 @level:system @id:AUTH-002-01
  Scenario: A principal cannot read another tenant's run
    Given tenants "acme" and "globex" each have a run
    When a principal of "acme" lists runs and fetches the "globex" run by id
    Then the list contains only "acme" runs
    And the fetch fails with code "not_found"

  @planned @REQ-AUTH-003 @wp:WP-L8-01 @level:component @id:AUTH-003-01
  Scenario Outline: Only valid issuer tokens are accepted
    Given auth mode "oidc" with issuer "https://id.example.test" and audience "kit"
    When a request carries a token that is <token>
    Then the request fails with code "unauthenticated"

    Examples:
      | token                          |
      | signed by an unknown key       |
      | issued for audience "other"    |
      | expired one minute ago         |

  @planned @REQ-AUTH-005 @wp:WP-L9-01 @level:system @id:AUTH-005-01
  Scenario: A worker token reaches only its own session
    Given the system issued a worker token for browser session "s1" expiring in 1 hour
    When the worker uses it to read browser session "s2"
    Then the request fails with code "permission_denied"

  @planned @REQ-AUTH-005 @wp:WP-L9-01 @level:system @id:AUTH-005-02
  Scenario: A worker token stops working when it expires
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And the system issued a worker token for browser session "s1" expiring in 1 hour
    When 61 minutes pass
    And the worker uses it to read browser session "s1"
    Then the request fails with code "unauthenticated"
