@planned @wp:WP-L1-04 @level:component
Feature: Key service and value encryption
  The secrets port opens its key service from a URL: a local key in the
  embedded profile, a KMS or Vault Transit in the cloud one. It encrypts small
  values, such as wrapped keysets and, later, credentials, with the master key.

  @security @REQ-SECRET-019 @id:SECRET-019-01
  Scenario Outline: An invalid key service URL is refused when the secrets port opens
    When the secrets port is opened with URL "<url>"
    Then opening fails with code "invalid_argument" and message "<message>"

    Examples:
      | url                                     | message                                    |
      | ftp://keys.example/mk-1                 | unsupported key service scheme "ftp"       |
      | localsecrets://                         | local key is missing                       |
      | localsecrets://bm90LWEtMzItYnl0ZS1rZXk= | local key must be 32 bytes, base64 encoded |
      | localsecrets://%%%not-base64            | local key must be 32 bytes, base64 encoded |

  @security @REQ-SECRET-019 @id:SECRET-019-02
  Scenario: A refused local key never appears in the error or the log
    Given the log is captured
    When the secrets port is opened with URL "localsecrets://bm90LWEtMzItYnl0ZS1rZXk="
    Then opening fails with code "invalid_argument"
    And neither the error nor the captured log contains "bm90LWEtMzItYnl0ZS1rZXk="

  @security @REQ-SECRET-020 @id:SECRET-020-01
  Scenario: A value encrypted by the secrets port decrypts with the same master key
    Given the secrets port is opened with a local key of 32 random bytes from seed 3
    When the value "sk-live-4f9a2c" is encrypted
    Then the ciphertext does not contain "sk-live-4f9a2c"
    And the ciphertext decrypts to "sk-live-4f9a2c"
    And the ciphertext decrypts to "sk-live-4f9a2c" after the secrets port is reopened with the same URL

  @security @REQ-SECRET-021 @id:SECRET-021-01
  Scenario Outline: A value ciphertext that was altered or made under another key does not decrypt
    Given the secrets port is opened with a local key of 32 random bytes from seed 3
    And the value "sk-live-4f9a2c" is encrypted
    When the ciphertext is decrypted after <change>
    Then decryption fails with code "invalid_argument"
    And no value is returned

    Examples:
      | change                                                   |
      | flipping one bit of the ciphertext                       |
      | cutting the ciphertext to its first 10 bytes             |
      | reopening the secrets port with a local key from seed 4  |
