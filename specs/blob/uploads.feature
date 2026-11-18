@planned @wp:WP-L1-08 @level:component
Feature: Blob upload URLs
  A large body is produced where the platform does not host it, so a client
  uploads it through a short-lived upload URL into a staging slot of its own
  tenant and commits it. Commit hashes the staged object on the server and
  stores it under its content key: a client never names a content key. The
  cloud profile hands out the provider's presigned PUTs; the embedded profile
  hands out proxied upload URLs that the upload URL handler serves and checks.
  Read URLs and upload URLs share the signing scheme and the maximum lifetime.

  Background:
    Given the clock is frozen at "2026-11-16T09:00:00Z"
    And an empty blob store with a maximum blob URL lifetime of 7 days

  @REQ-BLOB-021 @id:BLOB-021-01
  Scenario: An upload URL and a commit make the body a blob under its content key
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of "hello"
    And tenant "acme" reads "hello" under the content key of "hello"
    And the blob of tenant "acme" under the content key of "hello" has content type "text/plain"
    And tenant "acme" has no staged object under the staging key of the upload

  @REQ-BLOB-021 @security @id:BLOB-021-02
  Scenario: A client never names the content key of what it uploads
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    Then the staging key of the upload differs from the content key of "hello"
    And the upload URL names no content key
    When tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of "hello"

  @REQ-BLOB-021 @id:BLOB-021-03
  Scenario: An empty body commits to the content key of the empty content
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "" as "text/plain" to the upload URL
    And tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of ""
    And reading key "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" as tenant "acme" returns 0 bytes

  @REQ-BLOB-022 @security @id:BLOB-022-01
  Scenario Outline: Every upload gets a fresh staging slot
    When tenant "acme" starts two uploads of at most 1 MiB as "text/plain" with a lifetime of 15 minutes, <variation>
    Then the staging keys of the uploads differ
    And the upload URLs of the uploads differ

    Examples:
      | variation                     |
      | a minute apart                |
      | with the same options         |

  @REQ-BLOB-023 @id:BLOB-023-01
  Scenario Outline: Upload URL lifetimes outside the bounds are rejected
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of <lifetime>
    Then the call fails with code "invalid_argument"

    Examples:
      | lifetime        |
      | 0 seconds       |
      | -1 minute       |
      | 7 days 1 second |

  @REQ-BLOB-023 @id:BLOB-023-02
  Scenario: An upload URL may live exactly the maximum lifetime
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 7 days
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of "hello"

  @REQ-BLOB-023 @id:BLOB-023-03
  Scenario Outline: Upload options that are not bounds are rejected
    When tenant "acme" starts an upload with <options>
    Then the call fails with code "invalid_argument"

    Examples:
      | options                                     |
      | a maximum size of 0 bytes                   |
      | a maximum size of -1 bytes                  |
      | no content type                             |
      | content type "text"                         |
      | content type "text/plain, text/markdown"    |
      | content type "text/*"                       |

  @REQ-BLOB-024 @security @id:BLOB-024-01
  Scenario: A client without an upload URL cannot write to a staging slot
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL with its signature removed
    Then the response has status 403 and an empty body
    And tenant "acme" has no staged object under the staging key of the upload

  @REQ-BLOB-024 @security @profile:embedded @id:BLOB-024-02
  Scenario Outline: An altered proxied upload URL is refused
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL with <alteration>
    Then the response has status 403 and an empty body
    And tenant "acme" has no staged object under the staging key of the upload

    Examples:
      | alteration                                                |
      | its expiry moved 1 hour later                             |
      | its signature removed                                     |
      | its signature replaced by one from another signing key    |

  @REQ-BLOB-024 @security @profile:embedded @id:BLOB-024-03
  Scenario Outline: An altered proxied upload URL cannot change its bounds
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL with <alteration>
    Then the response has status 403 and an empty body
    And tenant "acme" has no staged object under the staging key of the upload

    Examples:
      | alteration                                   |
      | its maximum size replaced by 2 MiB           |
      | its content type replaced by "text/markdown" |

  @REQ-BLOB-024 @security @profile:embedded @id:BLOB-024-04
  Scenario: A proxied upload URL of one tenant cannot be turned into another tenant's
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And tenant "globex" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL with its tenant replaced by "globex"
    Then the response has status 403 and an empty body
    And tenant "globex" has no staged object under the staging key of the second upload

  @REQ-BLOB-025 @profile:embedded @id:BLOB-025-01
  Scenario Outline: A proxied upload URL waits for its lifetime and then expires
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And <elapsed> pass
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    Then the response has status <status> and <body>

    Examples:
      | elapsed               | status | body          |
      | 14 minutes 59 seconds | 201    | an empty body |
      | 15 minutes            | 403    | an empty body |

  @REQ-BLOB-025 @REQ-BLOB-026 @id:BLOB-025-02
  Scenario: An expired staging key is refused in both profiles
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And 15 minutes pass
    And tenant "acme" commits the staging key of the upload
    Then the call fails with code "not_found"

  @REQ-BLOB-026 @id:BLOB-026-01
  Scenario: An uncommitted upload is deleted when its lifetime ends
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And 15 minutes pass
    And the expired staged objects are swept
    Then tenant "acme" has no staged object under the staging key of the upload

  @REQ-BLOB-026 @REQ-BLOB-021 @id:BLOB-026-02
  Scenario: A sweep keeps staged objects that are still within their lifetime
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 1 hour
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And a client with no credentials uploads "world" as "text/plain" to the second upload URL
    And 15 minutes pass
    And the expired staged objects are swept
    And tenant "acme" commits the staging key of the second upload
    Then the commit returns the content key of "world"

  @REQ-BLOB-027 @id:BLOB-027-01
  Scenario: A body over the maximum size is refused at the URL and never committed
    Given content "big" of 8 MiB generated from seed 7
    When tenant "acme" starts an upload of at most 1 MiB as "application/octet-stream" with a lifetime of 15 minutes
    And a client with no credentials uploads content "big" as "application/octet-stream" to the upload URL
    Then the response has status 413 and an empty body
    And tenant "acme" has no staged object under the staging key of the upload

  @REQ-BLOB-028 @id:BLOB-028-01
  Scenario: A body of another content type is refused at the URL and never committed
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/markdown" to the upload URL
    Then the response has status 415 and an empty body
    And tenant "acme" has no staged object under the staging key of the upload

  @REQ-BLOB-029 @security @id:BLOB-029-01
  Scenario: Another tenant cannot commit a staged upload
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "globex" commits the staging key of the upload
    Then the call fails with code "not_found"
    And tenant "acme" has no blob under the content key of "hello"
    When tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of "hello"

  @REQ-BLOB-029 @REQ-BLOB-024 @security @id:BLOB-029-02
  Scenario: A staging key is unique across tenants
    When tenant "globex" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    Then the staging keys of the uploads differ

  @REQ-BLOB-030 @id:BLOB-030-01
  Scenario Outline: A staging key that was never issued is not found
    When tenant "acme" commits a staging key <origin>
    Then the call fails with code "not_found"

    Examples:
      | origin                                 |
      | never issued                           |
      | the content key of "hello"             |

  @REQ-BLOB-030 @id:BLOB-030-02
  Scenario: A staging key without a staged object is not found
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And tenant "acme" commits the staging key of the upload
    Then the call fails with code "not_found"

  @REQ-BLOB-031 @id:BLOB-031-01
  Scenario: A committed staging key cannot be committed again
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "acme" commits the staging key of the upload
    And tenant "acme" commits the staging key of the upload again
    Then the second commit fails with code "not_found"
    And tenant "acme" reads "hello" under the content key of "hello"

  @REQ-BLOB-031 @REQ-BLOB-024 @id:BLOB-031-02
  Scenario: A spent upload URL cannot be written to again
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "acme" commits the staging key of the upload
    And a client with no credentials uploads "world" as "text/plain" to the upload URL
    Then the response has status 403 and an empty body
    And tenant "acme" has no blob under the content key of "world"

  @REQ-BLOB-032 @security @id:BLOB-032-01
  Scenario: A staged object over the maximum size fails the commit and spends the staging key
    Given content "big" of 8 MiB generated from seed 7
    When tenant "acme" starts an upload of at most 1 MiB as "application/octet-stream" with a lifetime of 15 minutes
    And the staged object of the upload is content "big" as "application/octet-stream"
    And tenant "acme" commits the staging key of the upload
    Then the call fails with code "invalid_argument"
    And tenant "acme" has no staged object under the staging key of the upload
    And tenant "acme" has no blob under the content key of "big"
    And tenant "acme" commits the staging key of the upload again
    Then the call fails with code "not_found"

  @REQ-BLOB-032 @id:BLOB-032-02
  Scenario: A staged object of another content type fails the commit and is deleted
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And the staged object of the upload is "hello" as "text/markdown"
    And tenant "acme" commits the staging key of the upload
    Then the call fails with code "invalid_argument"
    And tenant "acme" has no staged object under the staging key of the upload
    And tenant "acme" has no blob under the content key of "hello"

  @REQ-BLOB-033 @id:BLOB-033-01
  Scenario Outline: A malformed staging key is rejected and touches nothing
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "acme" commits key "<key>"
    Then the call fails with code "invalid_argument"
    And tenant "acme" has staged an object under the staging key of the upload
    And tenant "acme" has no blob under the content key of "hello"

    Examples:
      | key                                     |
      |                                         |
      | ../../../etc/passwd                     |
      | a staging key with a path segment ".."  |
      | a staging key with a query string       |

  @REQ-BLOB-034 @id:BLOB-034-01
  Scenario: An upload cannot be started without a tenant
    When a caller with no tenant starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    Then the call fails with code "invalid_argument"

  @REQ-BLOB-034 @id:BLOB-034-02
  Scenario: A staging key cannot be committed without a tenant
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And a caller with no tenant commits the staging key of the upload
    Then the call fails with code "invalid_argument"
    And tenant "acme" has staged an object under the staging key of the upload
