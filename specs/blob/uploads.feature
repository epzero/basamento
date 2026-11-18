@planned @wp:WP-L1-08 @level:component
Feature: Blob upload URLs
  A large body is produced where the platform does not host it, so a client
  uploads it through a short-lived upload URL into a staging slot of its own
  tenant and commits it. Commit hashes the staged object on the server and
  stores it under its content key: a client never names a content key. The
  cloud profile hands out the provider's presigned PUTs, which enforce the
  signature, the expiry and the content type but not the size, so Commit
  checks the size and the content type of the staged object in both profiles.
  The embedded profile hands out proxied upload URLs that the upload URL
  handler serves and checks, and sweeps expired staging slots; in the cloud
  profile a bucket lifecycle rule expires them. Read URLs and upload URLs
  share the signing scheme and the maximum lifetime.

  Background:
    Given the clock is frozen at "2026-11-16T09:00:00Z"
    And an empty blob store with a maximum blob URL lifetime of 7 days

  @REQ-BLOB-021 @security @id:BLOB-021-01
  Scenario Outline: Every upload gets a fresh staging slot
    When tenant "acme" starts two uploads of at most 1 MiB as "text/plain" with a lifetime of 15 minutes, <apart>
    Then the staging keys of the uploads differ
    And the upload URLs of the uploads differ

    Examples:
      | apart                  |
      | one after the other    |
      | a minute apart         |

  @REQ-BLOB-022 @id:BLOB-022-01
  Scenario Outline: Upload URL lifetimes outside the bounds are rejected
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of <lifetime>
    Then the call fails with code "invalid_argument"

    Examples:
      | lifetime        |
      | 0 seconds       |
      | -1 minute       |
      | 7 days 1 second |

  @REQ-BLOB-022 @id:BLOB-022-02
  Scenario: An upload URL may live exactly the maximum lifetime
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 7 days
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of "hello"

  @REQ-BLOB-022 @id:BLOB-022-03
  Scenario Outline: Upload options that are not bounds are rejected
    When tenant "acme" starts an upload with <options>
    Then the call fails with code "invalid_argument"

    Examples:
      | options                                  |
      | a maximum size of 0 bytes                |
      | a maximum size of -1 bytes               |
      | no content type                          |
      | content type "text"                      |
      | content type "text/plain, text/markdown" |
      | content type "text/*"                    |

  @REQ-BLOB-023 @security @profile:embedded @id:BLOB-023-01
  Scenario: A proxied upload URL signed for one staging key cannot write to another
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL with its staging key replaced by the staging key of the second upload
    Then the response has status 403 and an empty body
    And tenant "acme" has no staged object under the staging key of the second upload

  @REQ-BLOB-023 @profile:embedded @id:BLOB-023-02
  Scenario Outline: An altered proxied upload URL is refused
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL with <alteration>
    Then the response has status 403 and an empty body
    And tenant "acme" has no staged object under the staging key of the upload

    Examples:
      | alteration                                             |
      | its expiry moved 1 hour later                          |
      | its signature removed                                  |
      | its signature replaced by one from another signing key |

  @REQ-BLOB-023 @profile:embedded @id:BLOB-023-03
  Scenario Outline: An altered proxied upload URL cannot change its bounds
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL with <alteration>
    Then the response has status 403 and an empty body
    And tenant "acme" has no staged object under the staging key of the upload

    Examples:
      | alteration                                   |
      | its maximum size replaced by 2 MiB           |
      | its content type replaced by "text/markdown" |

  @REQ-BLOB-023 @security @profile:embedded @id:BLOB-023-04
  Scenario: A proxied upload URL of one tenant cannot be turned into another tenant's
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And tenant "globex" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL with its tenant replaced by "globex"
    Then the response has status 403 and an empty body
    And tenant "acme" has no staged object under the staging key of the first upload
    And tenant "globex" has no staged object under the staging key of the first upload

  @REQ-BLOB-024 @profile:embedded @id:BLOB-024-01
  Scenario: A body of exactly the maximum size is accepted
    Given content "big" of 1 MiB generated from seed 7
    When tenant "acme" starts an upload of at most 1 MiB as "application/octet-stream" with a lifetime of 15 minutes
    And a client with no credentials uploads content "big" as "application/octet-stream" to the upload URL
    Then the response has status 201 and an empty body
    And tenant "acme" has a staged object of content "big" as "application/octet-stream" under the staging key of the upload

  @REQ-BLOB-025 @profile:embedded @id:BLOB-025-01
  Scenario: Replaying an upload replaces the staged object
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And a client with no credentials uploads "world" as "text/plain" to the upload URL
    Then the response has status 201 and an empty body
    And tenant "acme" has a staged object of content "world" under the staging key of the upload
    When tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of "world"
    And tenant "acme" has no blob under the content key of "hello"

  @REQ-BLOB-026 @security @profile:embedded @id:BLOB-026-01
  Scenario Outline: A proxied upload URL refuses a missing or spent staging key
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And <before>
    And a client with no credentials uploads "world" as "text/plain" to the upload URL
    Then the response has status 403 and an empty body

    Examples:
      | before                                              |
      | the staged object of the upload is deleted          |
      | tenant "acme" commits the staging key of the upload |
      | the staged object of the upload fails to commit     |

  @REQ-BLOB-027 @security @profile:embedded @id:BLOB-027-01
  Scenario Outline: A proxied upload URL works until its lifetime ends
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And <elapsed> pass
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    Then the response has status <status> and <body>

    Examples:
      | elapsed               | status | body          |
      | 14 minutes 59 seconds | 201    | an empty body |
      | 15 minutes            | 403    | an empty body |

  @REQ-BLOB-028 @id:BLOB-028-01
  Scenario: A staging key committed after its lifetime ends is not found
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And 15 minutes pass
    And tenant "acme" commits the staging key of the upload
    Then the call fails with code "not_found"
    And tenant "acme" has no blob under the content key of "hello"

  @REQ-BLOB-029 @id:BLOB-029-01
  Scenario: An upload URL and a commit make the body a blob under its content key
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of "hello"
    And tenant "acme" reads "hello" under the content key of "hello"
    And the blob of tenant "acme" under the content key of "hello" has content type "text/plain"
    And tenant "acme" has no staged object under the staging key of the upload

  @REQ-BLOB-029 @security @id:BLOB-029-02
  Scenario: A client never names the content key of what it uploads
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    Then the staging key of the upload differs from the content key of "hello"
    And the upload URL names no content key
    When tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of "hello"

  @REQ-BLOB-029 @id:BLOB-029-03
  Scenario: An empty body commits to the content key of the empty content
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "" as "text/plain" to the upload URL
    And tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of ""
    And reading key "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" as tenant "acme" returns 0 bytes

  @REQ-BLOB-030 @profile:embedded @id:BLOB-030-01
  Scenario: An uncommitted upload is deleted when its lifetime ends
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And 15 minutes pass
    And the expired staging slots are swept
    Then tenant "acme" has no staged object under the staging key of the upload
    And tenant "acme" has no commit record for the staging key of the upload

  @REQ-BLOB-030 @REQ-BLOB-029 @profile:embedded @id:BLOB-030-02
  Scenario: A sweep keeps staging slots that are still within their lifetime
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 1 hour
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And a client with no credentials uploads "world" as "text/plain" to the second upload URL
    And 15 minutes pass
    And the expired staging slots are swept
    And tenant "acme" commits the staging key of the second upload
    Then the commit returns the content key of "world"

  @REQ-BLOB-031 @profile:embedded @id:BLOB-031-01
  Scenario: A proxied upload URL refuses a body over the maximum size
    Given content "big" of 8 MiB generated from seed 7
    When tenant "acme" starts an upload of at most 1 MiB as "application/octet-stream" with a lifetime of 15 minutes
    And a client with no credentials uploads content "big" as "application/octet-stream" to the upload URL
    Then the response has status 413 and an empty body
    And tenant "acme" has no staged object under the staging key of the upload

  @REQ-BLOB-032 @profile:embedded @id:BLOB-032-01
  Scenario: A proxied upload URL refuses a body of another content type
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/markdown" to the upload URL
    Then the response has status 415 and an empty body
    And tenant "acme" has no staged object under the staging key of the upload

  @REQ-BLOB-033 @security @id:BLOB-033-01
  Scenario: A commit refuses a staged object over the maximum size in both profiles
    Given tenant "acme" has stored "hello" as "text/plain"
    When tenant "acme" starts an upload of at most 1 MiB as "application/octet-stream" with a lifetime of 15 minutes
    And the staged object of the upload is the stored "hello" followed by 1 MiB generated from seed 7
    And tenant "acme" commits the staging key of the upload
    Then the call fails with code "invalid_argument"
    And tenant "acme" has no blob under the content key "sha256:2a21fe6d592a19b7de898b50eb53c429608de1a66f3e9f62da19714a770553d1"

  @REQ-BLOB-034 @id:BLOB-034-01
  Scenario: A commit refuses a staged object of another content type in both profiles
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And the staged object of the upload is "hello" as "text/markdown"
    And tenant "acme" commits the staging key of the upload
    Then the call fails with code "invalid_argument"
    And tenant "acme" has no blob under the content key of "hello"

  @REQ-BLOB-035 @security @id:BLOB-035-01
  Scenario: Another tenant cannot commit a staged upload
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "globex" commits the staging key of the upload
    Then the call fails with code "not_found"
    And tenant "globex" has no blob under the content key of "hello"
    When tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of "hello"

  @REQ-BLOB-036 @REQ-BLOB-021 @security @id:BLOB-036-01
  Scenario: Two tenants never share a staging key
    When tenant "globex" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    Then the staging keys of the uploads differ

  @REQ-BLOB-037 @id:BLOB-037-01
  Scenario: A staging key no upload issued is not found
    When tenant "acme" commits the staging key "stage_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
    Then the call fails with code "not_found"

  @REQ-BLOB-038 @id:BLOB-038-01
  Scenario: A staging key without a staged object is not found
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And tenant "acme" commits the staging key of the upload
    Then the call fails with code "not_found"

  @REQ-BLOB-039 @id:BLOB-039-01
  Scenario: A committed staging key cannot be committed again
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "acme" commits the staging key of the upload
    And tenant "acme" commits the staging key of the upload again
    Then the second commit fails with code "not_found"
    And tenant "acme" reads "hello" under the content key of "hello"

  @REQ-BLOB-040 @security @id:BLOB-040-01
  Scenario Outline: A failed commit deletes the staged object and spends the staging key
    When tenant "acme" starts an upload of at most 1 MiB as <content type> with a lifetime of 15 minutes
    And the staged object of the upload is <staged object>
    And tenant "acme" commits the staging key of the upload
    Then the call fails with code "invalid_argument"
    And tenant "acme" has no staged object under the staging key of the upload
    And tenant "acme" commits the staging key of the upload again
    Then the call fails with code "not_found"

    Examples:
      | staged object                       | content type             | staged type              | content key             |
      | the stored "hello" followed by 1 MiB generated from seed 7 | application/octet-stream | application/octet-stream | sha256:2a21fe6d592a19b7de898b50eb53c429608de1a66f3e9f62da19714a770553d1 |
      | "world"                             | text/plain               | text/markdown            | sha256:486ea46224d1bb4fb680f34f7c9ad96a8f24ec88be73ea8e5a6c65260e9cb8a7 |

  @REQ-BLOB-040 @REQ-BLOB-030 @profile:embedded @id:BLOB-040-02
  Scenario: A spent staging key stays spent after the sweep of its expired slot
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And the staged object of the upload is "hello" as "text/markdown"
    And tenant "acme" commits the staging key of the upload
    And 15 minutes pass
    And the expired staging slots are swept
    And tenant "acme" commits the staging key of the upload again
    Then the second commit fails with code "not_found"

  @REQ-BLOB-041 @id:BLOB-041-01
  Scenario Outline: A malformed staging key is rejected and touches nothing
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And tenant "acme" commits key "<key>"
    Then the call fails with code "invalid_argument"
    And tenant "acme" has a staged object of content "hello" as "text/plain" under the staging key of the upload
    And tenant "acme" has no blob under the content key of "hello"

    Examples:
      | key                                                                     |
      |                                                                         |
      | sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824 |
      | stage_../../x                                                           |
      | stage_abc?x=1                                                           |
      | stage_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa                        |
      | stage_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa                      |
      | STAGE_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA                       |

  @REQ-BLOB-042 @security @id:BLOB-042-01
  Scenario: An upload cannot be started without a tenant
    When a caller with no tenant starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    Then the call fails with code "invalid_argument"

  @REQ-BLOB-042 @security @id:BLOB-042-02
  Scenario: A staging key cannot be committed without a tenant
    When tenant "acme" starts an upload of at most 1 MiB as "text/plain" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/plain" to the upload URL
    And a caller with no tenant commits the staging key of the upload
    Then the call fails with code "invalid_argument"
    And tenant "acme" has a staged object of content "hello" as "text/plain" under the staging key of the upload

  @REQ-BLOB-043 @id:BLOB-043-01
  Scenario: Committing content the tenant already holds keeps the stored blob
    Given tenant "acme" has stored "hello" as "text/plain"
    When tenant "acme" starts an upload of at most 1 MiB as "text/markdown" with a lifetime of 15 minutes
    And a client with no credentials uploads "hello" as "text/markdown" to the upload URL
    And tenant "acme" commits the staging key of the upload
    Then the commit returns the content key of "hello"
    And the blob of tenant "acme" under the content key of "hello" has content type "text/plain"
    And tenant "acme" reads "hello" under the content key of "hello"
