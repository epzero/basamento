@planned @wp:WP-L1-02 @level:component
Feature: Storing, reading and deleting blobs
  Blobs are immutable and named by their content key, so the same content is
  stored once per tenant and a reader can verify what it got. Every scenario
  runs against the local directory and against the S3 simulator.

  Background:
    Given an empty blob store

  @REQ-BLOB-001 @id:BLOB-001-01
  Scenario: A blob is stored under the SHA-256 hash of its content
    When tenant "acme" stores "hello" as "text/plain"
    Then the write succeeds
    And tenant "acme" has a blob under key "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

  @REQ-BLOB-001 @REQ-BLOB-004 @id:BLOB-001-02
  Scenario: Empty content has a content key and reads back empty
    When tenant "acme" stores "" as "text/plain"
    Then tenant "acme" has a blob under key "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    And reading key "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" as tenant "acme" returns 0 bytes

  @REQ-BLOB-002 @id:BLOB-002-01
  Scenario: Content written under another content's key is rejected
    When tenant "acme" stores "hello" under the content key of "world"
    Then the call fails with code "invalid_argument"
    And tenant "acme" has no blob under the content key of "world"
    And tenant "acme" has no blob under the content key of "hello"

  @REQ-BLOB-002 @id:BLOB-002-02
  Scenario: A mismatch in the last byte of a large write keeps nothing
    Given content "big" of 8 MiB generated from seed 7
    When tenant "acme" stores content "big" with its last byte changed under the content key of "big"
    Then the call fails with code "invalid_argument"
    And tenant "acme" has no blob under the content key of "big"

  @REQ-BLOB-002 @id:BLOB-002-03
  Scenario: Content mismatch under an existing key is rejected without harming the stored blob
    Given tenant "acme" has stored "hello" as "text/plain"
    When tenant "acme" stores "world" under the content key of "hello"
    Then the call fails with code "invalid_argument"
    And tenant "acme" reads "hello" under the content key of "hello"
    And the blob of tenant "acme" under the content key of "hello" has content type "text/plain"

  @REQ-BLOB-003 @security @id:BLOB-003-01
  Scenario Outline: Malformed keys are rejected
    When tenant "acme" reads key "<key>"
    Then the call fails with code "invalid_argument"

    Examples:
      | key                                                                     |
      |                                                                         |
      | 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824        |
      | sha256:2CF24DBA5FB0A30E26E83B2AC5B9E29E1B161E5C1FA7425E73043362938B9824 |
      | sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b982  |
      | sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b98240 |
      | md5:5d41402abc4b2a76b9719d911017c592                                    |
      | sha256:../../../etc/passwd                                              |

  @REQ-BLOB-003 @security @id:BLOB-003-02
  Scenario Outline: Every operation rejects a malformed key and touches nothing
    Given tenant "acme" has stored "hello" as "text/plain"
    When tenant "acme" <operation> key "sha256:../2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
    Then the call fails with code "invalid_argument"
    And tenant "acme" reads "hello" under the content key of "hello"

    Examples:
      | operation                   |
      | stores "hello" under        |
      | checks the existence of     |
      | deletes                     |
      | requests a blob URL for     |

  @REQ-BLOB-004 @id:BLOB-004-01
  Scenario: A large blob reads back byte for byte
    Given content "big" of 8 MiB generated from seed 7
    When tenant "acme" stores content "big" as "application/octet-stream"
    Then reading the content key of "big" as tenant "acme" returns content "big" exactly

  @REQ-BLOB-005 @id:BLOB-005-01
  Scenario: Storing the same content again keeps the first blob
    Given tenant "acme" has stored "hello" as "text/plain"
    When tenant "acme" stores "hello" as "application/octet-stream"
    Then the write succeeds
    And the blob of tenant "acme" under the content key of "hello" has content type "text/plain"
    And tenant "acme" reads "hello" under the content key of "hello"

  @REQ-BLOB-006 @id:BLOB-006-01
  Scenario Outline: Calls on a missing blob fail with not_found
    When tenant "acme" <operation> the content key of "hello"
    Then the call fails with code "not_found"

    Examples:
      | operation               |
      | reads                   |
      | deletes                 |
      | requests a blob URL for |

  @REQ-BLOB-007 @id:BLOB-007-01
  Scenario: Existence is answered per content key
    Given tenant "acme" has stored "hello" as "text/plain"
    Then tenant "acme" has a blob under the content key of "hello"
    And tenant "acme" has no blob under the content key of "world"

  @REQ-BLOB-008 @id:BLOB-008-01
  Scenario: A deleted blob is gone
    Given tenant "acme" has stored "hello" as "text/plain"
    When tenant "acme" deletes the content key of "hello"
    Then tenant "acme" has no blob under the content key of "hello"
    And reading the content key of "hello" as tenant "acme" fails with code "not_found"

  @REQ-BLOB-008 @REQ-BLOB-005 @id:BLOB-008-02
  Scenario: Deleted content can be stored again
    Given tenant "acme" has stored "hello" as "text/plain"
    And tenant "acme" has deleted the content key of "hello"
    When tenant "acme" stores "hello" as "text/markdown"
    Then tenant "acme" reads "hello" under the content key of "hello"
    And the blob of tenant "acme" under the content key of "hello" has content type "text/markdown"

  @REQ-BLOB-009 @id:BLOB-009-01
  Scenario Outline: An interrupted write keeps nothing
    Given content "big" of 8 MiB generated from seed 7
    When tenant "acme" stores content "big" and <interruption> after 1 MiB
    Then the write fails
    And tenant "acme" has no blob under the content key of "big"

    Examples:
      | interruption           |
      | the source fails       |
      | the call is cancelled  |

  @REQ-BLOB-009 @REQ-BLOB-012 @id:BLOB-009-02
  Scenario: An interrupted write leaves nothing behind after a restart
    Given content "big" of 8 MiB generated from seed 7
    And tenant "acme" stores content "big" and the source fails after 1 MiB
    When the blob store is closed and opened again on the same location
    Then tenant "acme" has no blob under the content key of "big"

  @REQ-BLOB-010 @security @id:BLOB-010-01
  Scenario Outline: Another tenant cannot reach a blob
    Given tenant "acme" has stored "hello" as "text/plain"
    When tenant "globex" <operation> the content key of "hello"
    Then <outcome>
    And tenant "acme" reads "hello" under the content key of "hello"

    Examples:
      | operation               | outcome                                  |
      | reads                   | the call fails with code "not_found"     |
      | checks the existence of | the answer is that no blob exists        |
      | deletes                 | the call fails with code "not_found"     |
      | requests a blob URL for | the call fails with code "not_found"     |

  @REQ-BLOB-010 @security @id:BLOB-010-02
  Scenario: The same content stored by two tenants stays separate
    Given tenant "acme" has stored "hello" as "text/plain"
    And tenant "globex" has stored "hello" as "text/markdown"
    When tenant "globex" deletes the content key of "hello"
    Then tenant "globex" has no blob under the content key of "hello"
    And tenant "acme" reads "hello" under the content key of "hello"
    And the blob of tenant "acme" under the content key of "hello" has content type "text/plain"

  @REQ-BLOB-011 @security @id:BLOB-011-01
  Scenario Outline: A call without a tenant is rejected
    Given tenant "acme" has stored "hello" as "text/plain"
    When a caller with no tenant <operation> the content key of "hello"
    Then the call fails with code "invalid_argument"
    And tenant "acme" reads "hello" under the content key of "hello"

    Examples:
      | operation                   |
      | stores "hello" under        |
      | reads                       |
      | checks the existence of     |
      | deletes                     |
      | requests a blob URL for     |

  @REQ-BLOB-012 @id:BLOB-012-01
  Scenario: Blobs survive a restart and deletions stay deleted
    Given tenant "acme" has stored "hello" as "text/plain"
    And tenant "acme" has stored "world" as "text/plain"
    And tenant "acme" has deleted the content key of "world"
    When the blob store is closed and opened again on the same location
    Then tenant "acme" reads "hello" under the content key of "hello"
    And the blob of tenant "acme" under the content key of "hello" has content type "text/plain"
    And tenant "acme" has no blob under the content key of "world"
