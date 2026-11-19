@wp:WP-L1-04 @level:component
Feature: Payload encryption
  The payload codec encrypts every payload a runtime records in history (step
  inputs and results, signals, gate payloads), so history holds no plaintext
  (constitution, article 7). A payload is encrypted with the tenant's primary
  data key and authenticated together with the tenant and the data key id.

  Background:
    Given the simulated key service holds master key "mk-1"
    And a payload codec with an empty keyset store and master key "mk-1"

  @security @REQ-SECRET-001 @id:SECRET-001-01
  Scenario: A payload decodes to the data and metadata it was encoded from
    Given payload "p1" with encoding "json/plain" and data:
      """
      {"prompt":"Summarise the contract","api_key":"sk-live-4f9a2c"}
      """
    When payload "p1" is encoded for tenant "acme"
    And encoded payload "p1" is decoded for tenant "acme"
    Then the decoded payload has encoding "json/plain" and no other metadata
    And the decoded payload data equals the data of payload "p1" byte for byte

  @security @REQ-SECRET-001 @id:SECRET-001-02
  Scenario Outline: Payloads of any size round trip
    Given payload "p1" with encoding "binary/plain" and <size> random bytes from seed 7
    When payload "p1" is encoded for tenant "acme"
    And encoded payload "p1" is decoded for tenant "acme"
    Then the decoded payload data equals the data of payload "p1" byte for byte

    Examples:
      | size    |
      | 0       |
      | 1       |
      | 1048576 |

  @security @REQ-SECRET-002 @id:SECRET-002-01
  Scenario: An encoded payload shows only the encryption marker, the data key id and ciphertext
    Given payload "p1" with encoding "json/plain" and data:
      """
      {"api_key":"sk-live-4f9a2c"}
      """
    When payload "p1" is encoded for tenant "acme"
    Then encoded payload "p1" has exactly the metadata keys "encoding" and "encryption-key-id"
    And encoded payload "p1" has encoding "binary/encrypted"
    And encoded payload "p1" names the primary data key of tenant "acme"
    And the data of encoded payload "p1" contains neither "sk-live-4f9a2c" nor "json/plain"

  @security @REQ-SECRET-003 @id:SECRET-003-01
  Scenario: Encoding the same payload twice gives two different ciphertexts
    Given payload "p1" with encoding "json/plain" and data "{\"approved\":true}"
    When payload "p1" is encoded for tenant "acme" as "first"
    And payload "p1" is encoded for tenant "acme" as "second"
    Then encoded payloads "first" and "second" have different data
    And encoded payloads "first" and "second" both decode for tenant "acme" to the data of payload "p1"

  @security @REQ-SECRET-004 @id:SECRET-004-01
  Scenario Outline: A tampered payload is rejected without saying which check failed
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And the data key of tenant "acme" has been rotated once since
    When encoded payload "p1" is tampered with: <change>
    And encoded payload "p1" is decoded for tenant "acme"
    Then decoding fails with code "invalid_argument" and message "payload cannot be decrypted"
    And no decoded payload is returned

    Examples:
      | change                                                            |
      | one bit of the ciphertext is flipped                              |
      | the last byte of the ciphertext is removed                        |
      | the ciphertext is cut to its first 10 bytes                       |
      | the ciphertext is emptied                                         |
      | the data key id is replaced by the primary data key id of "acme"  |
      | the data key id is replaced by an id no keyset holds              |
      | the data key id is removed                                        |

  @security @REQ-SECRET-005 @REQ-SECRET-004 @id:SECRET-005-01
  Scenario: A payload encoded for one tenant does not decode for another
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And payload "p2" with encoding "json/plain" and data "{}" encoded for tenant "globex"
    When encoded payload "p1" is decoded for tenant "globex"
    Then decoding fails with code "invalid_argument" and message "payload cannot be decrypted"
    And no decoded payload is returned

  @security @REQ-SECRET-005 @REQ-SECRET-004 @id:SECRET-005-02
  Scenario: Decoding for a tenant without a keyset fails the same way and creates none
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    When encoded payload "p1" is decoded for tenant "initech"
    Then decoding fails with code "invalid_argument" and message "payload cannot be decrypted"
    And the keyset store holds no keyset for tenant "initech"

  @security @REQ-SECRET-006 @id:SECRET-006-01
  Scenario: A payload that was never encrypted is refused on decode
    Given payload "raw" with encoding "json/plain" and data "{\"approved\":true}"
    When payload "raw" is decoded for tenant "acme" without being encoded
    Then decoding fails with code "invalid_argument" and message "payload is not encrypted"
    And no decoded payload is returned

  @security @REQ-SECRET-007 @id:SECRET-007-01
  Scenario: A failed decode reveals no data and no key in its error or the log
    Given the log is captured
    And payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    When encoded payload "p1" is tampered with: one bit of the ciphertext is flipped
    And encoded payload "p1" is decoded for tenant "acme"
    Then decoding fails with code "invalid_argument"
    And neither the error nor the captured log contains "sk-live-4f9a2c"
    And neither the error nor the captured log contains the bytes of a data key of tenant "acme" or of master key "mk-1"

  @security @REQ-SECRET-007 @id:SECRET-007-02
  Scenario: Encoding and decoding log no data and no key
    Given the log is captured
    And payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    When encoded payload "p1" is decoded for tenant "acme"
    Then the captured log does not contain "sk-live-4f9a2c"
    And the captured log does not contain the bytes of a data key of tenant "acme" or of master key "mk-1"

  @security @REQ-SECRET-022 @id:SECRET-022-01
  Scenario: Encoding a payload without a tenant is rejected
    Given payload "p1" with encoding "json/plain" and data "{}"
    When payload "p1" is encoded without a tenant
    Then encoding fails with code "invalid_argument"
    And no encoded payload is returned

  @security @REQ-SECRET-022 @id:SECRET-022-02
  Scenario: Decoding an encoded payload without a tenant is rejected
    Given payload "p1" with encoding "json/plain" and data "{}" encoded for tenant "acme"
    When encoded payload "p1" is decoded without a tenant
    Then decoding fails with code "invalid_argument"
    And no decoded payload is returned
