@planned @wp:WP-L1-02 @level:component
Feature: Opening a blob store from its location URL
  Configuration names the blob store by a location URL. The scheme selects the
  implementation, so the embedded and cloud profiles differ only in that URL.

  @REQ-BLOB-018 @id:BLOB-018-01
  Scenario Outline: The scheme selects where blobs are kept
    When a blob store is opened from <location>
    And tenant "acme" stores "hello" as "text/plain"
    Then the content "hello" is kept in <place>

    Examples:
      | location                                      | place                         |
      | a file URL of a temporary directory           | the temporary directory       |
      | an s3 URL of a bucket in the S3 simulator     | that bucket of the S3 simulator |

  @REQ-BLOB-018 @live @id:BLOB-018-02
  Scenario Outline: Cloud stores open from their schemes
    When a blob store is opened from <location>
    And tenant "acme" stores "hello" as "text/plain"
    Then tenant "acme" reads "hello" under the content key of "hello"

    Examples:
      | location                                  |
      | a gs URL of the nightly test bucket       |
      | an azblob URL of the nightly test container |

  @REQ-BLOB-019 @id:BLOB-019-01
  Scenario Outline: Unsupported locations are refused
    When a blob store is opened from "<url>"
    Then the call fails with code "invalid_argument"
    And the error message names the schemes "file", "s3", "gs" and "azblob"

    Examples:
      | url                     |
      | mem://                  |
      | ftp://example.test/blobs |
      | /var/lib/kit/blobs      |
      |                         |

  @REQ-BLOB-020 @id:BLOB-020-01
  Scenario: Opening on a directory that does not exist creates it empty
    Given a temporary directory with no subdirectory "blobs"
    When a blob store is opened from a file URL of its subdirectory "blobs"
    Then the subdirectory "blobs" exists
    And tenant "acme" has no blob under the content key of "hello"

  @REQ-BLOB-045 @security @planned @wp:WP-L1-02 @id:BLOB-045-01
  Scenario Outline: A signing key shorter than 32 bytes is refused
    When a blob store is opened from a file URL of a temporary directory with a signing key of <key> bytes
    Then <outcome>

    Examples:
      | key | outcome                                     |
      | 31  | the call fails with code "invalid_argument" |
      | 32  | the store is open                           |

  @REQ-BLOB-045 @planned @wp:WP-L1-02 @id:BLOB-045-02
  Scenario: A store opened without a signing key issues no proxied blob URL
    Given a blob store is opened from a file URL of a temporary directory without a signing key
    And tenant "acme" has stored "hello" as "text/plain"
    When tenant "acme" requests a blob URL for the content key of "hello" with a lifetime of 15 minutes
    Then the call fails with code "internal"
