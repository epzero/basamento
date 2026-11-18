@wp:WP-L1-02 @level:component
Feature: Blob URLs
  A blob URL lets a client without credentials, such as a browser or a
  worker, fetch one blob for a limited time. The cloud profile hands out the
  provider's signed URLs; the embedded profile hands out proxied blob URLs that
  the blob URL handler serves and checks.

  Background:
    Given the clock is frozen at "2026-11-16T09:00:00Z"
    And an empty blob store with a maximum blob URL lifetime of 7 days

  @REQ-BLOB-013 @id:BLOB-013-01
  Scenario: A blob URL serves the blob to a client without credentials
    Given tenant "acme" has stored "hello" as "text/plain"
    When tenant "acme" requests a blob URL for the content key of "hello" with a lifetime of 15 minutes
    And a client with no credentials fetches the blob URL
    Then the response has status 200, content type "text/plain" and body "hello"

  @REQ-BLOB-014 @id:BLOB-014-01
  Scenario: A blob stored without a content type is served as octet-stream
    Given tenant "acme" has stored "hello" with no content type
    When tenant "acme" requests a blob URL for the content key of "hello" with a lifetime of 15 minutes
    And a client with no credentials fetches the blob URL
    Then the response has status 200, content type "application/octet-stream" and body "hello"

  @REQ-BLOB-015 @REQ-BLOB-013 @security @profile:embedded @id:BLOB-015-01
  Scenario Outline: A proxied blob URL works until its lifetime ends
    Given tenant "acme" has stored "hello" as "text/plain"
    And tenant "acme" has a blob URL for the content key of "hello" with a lifetime of 15 minutes
    When <elapsed> pass
    And a client with no credentials fetches the blob URL
    Then the response has status <status> and <body>

    Examples:
      | elapsed               | status | body          |
      | 14 minutes 59 seconds | 200    | body "hello"  |
      | 15 minutes            | 403    | an empty body |
      | 8 days                | 403    | an empty body |

  @REQ-BLOB-016 @security @profile:embedded @id:BLOB-016-01
  Scenario Outline: An altered proxied blob URL is refused
    Given tenant "acme" has stored "hello" as "text/plain"
    And tenant "acme" has stored "world" as "text/plain"
    And tenant "acme" has a blob URL for the content key of "hello" with a lifetime of 15 minutes
    When a client fetches the blob URL with <alteration>
    Then the response has status 403 and an empty body

    Examples:
      | alteration                                         |
      | its key replaced by the content key of "world"     |
      | its expiry moved 1 hour later                      |
      | its signature removed                              |
      | its signature replaced by one from another signing key |

  @REQ-BLOB-016 @REQ-BLOB-010 @security @profile:embedded @id:BLOB-016-02
  Scenario: A proxied blob URL of one tenant cannot be turned into another tenant's
    Given tenant "acme" has stored "hello" as "text/plain"
    And tenant "globex" has stored "hello" as "text/markdown"
    And tenant "acme" has a blob URL for the content key of "hello" with a lifetime of 15 minutes
    When a client fetches the blob URL with its tenant replaced by "globex"
    Then the response has status 403 and an empty body

  @REQ-BLOB-008 @id:BLOB-008-03
  Scenario: A blob URL stops serving once its blob is deleted
    Given tenant "acme" has stored "hello" as "text/plain"
    And tenant "acme" has a blob URL for the content key of "hello" with a lifetime of 15 minutes
    When tenant "acme" deletes the content key of "hello"
    And a client with no credentials fetches the blob URL
    Then the response has status 404 and its body does not contain "hello"

  @REQ-BLOB-017 @id:BLOB-017-01
  Scenario Outline: Blob URL lifetimes outside the bounds are rejected
    Given tenant "acme" has stored "hello" as "text/plain"
    When tenant "acme" requests a blob URL for the content key of "hello" with a lifetime of <lifetime>
    Then the call fails with code "invalid_argument"

    Examples:
      | lifetime        |
      | 0 seconds       |
      | -1 minute       |
      | 7 days 1 second |

  @REQ-BLOB-017 @REQ-BLOB-013 @id:BLOB-017-02
  Scenario: A blob URL may live exactly the maximum lifetime
    Given tenant "acme" has stored "hello" as "text/plain"
    When tenant "acme" requests a blob URL for the content key of "hello" with a lifetime of 7 days
    And a client with no credentials fetches the blob URL
    Then the response has status 200, content type "text/plain" and body "hello"

  @REQ-BLOB-044 @security @profile:embedded @planned @wp:WP-L1-02 @id:BLOB-044-01
  Scenario: A proxied read of stored HTML is served as an inert attachment
    Given tenant "acme" has stored "<h1>hello</h1>" as "text/html"
    And tenant "acme" has a blob URL for the content key of "<h1>hello</h1>" with a lifetime of 15 minutes
    When a client with no credentials fetches the blob URL
    Then the response has status 200, content type "text/html" and body "<h1>hello</h1>"
    And the response carries these headers:
      | header                  | value                                                                                    |
      | Content-Disposition     | attachment; filename="4db7ef630005c462450ea587722b1a7cff53dfdcd35d7dd40bcf8e97e50826ee" |
      | Content-Security-Policy | sandbox; default-src 'none'                                                              |
      | Cache-Control           | private, no-store                                                                        |
      | X-Content-Type-Options  | nosniff                                                                                  |

  @REQ-BLOB-016 @REQ-BLOB-010 @security @profile:embedded @planned @wp:WP-L1-02 @id:BLOB-016-03
  Scenario Outline: A proxied blob URL serves a tenant whose id needs escaping
    Given tenant "<tenant>" has stored "hello" as "text/plain"
    And tenant "<tenant>" has a blob URL for the content key of "hello" with a lifetime of 15 minutes
    When a client with no credentials fetches the blob URL
    Then the response has status 200, content type "text/plain" and body "hello"

    Examples:
      | tenant  |
      | a%b     |
      | a b     |
      | a?b     |
      | a#b     |
      | aéb     |

  @REQ-BLOB-016 @REQ-BLOB-010 @security @profile:embedded @planned @wp:WP-L1-02 @id:BLOB-016-04
  Scenario: A proxied blob URL of an escaped tenant cannot be turned into another tenant's
    Given tenant "a b" has stored "hello" as "text/plain"
    And tenant "globex" has stored "hello" as "text/markdown"
    And tenant "a b" has a blob URL for the content key of "hello" with a lifetime of 15 minutes
    When a client fetches the blob URL with its tenant replaced by "globex"
    Then the response has status 403 and an empty body
