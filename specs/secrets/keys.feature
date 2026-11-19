@planned @wp:WP-L1-04 @level:component
Feature: Data keys and master keys
  Each tenant has a keyset of data keys, one of them primary. The keyset is
  stored only wrapped by the master key, which never leaves the key service.
  Data keys and the master key rotate without making any payload in history
  unreadable.

  Background:
    Given the simulated key service holds master key "mk-1"
    And a payload codec with an empty keyset store and master key "mk-1"

  @security @REQ-SECRET-008 @id:SECRET-008-01
  Scenario: A tenant's keyset is stored only wrapped by the master key
    When payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" is encoded for tenant "acme"
    Then the keyset store holds 1 keyset for tenant "acme"
    And the stored keyset of tenant "acme" names master key "mk-1" as the key that wrapped it
    And the stored keyset of tenant "acme" contains the bytes of none of its data keys

  @security @REQ-SECRET-009 @id:SECRET-009-01
  Scenario: Payloads encoded before a restart decode after it
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    When the payload codec restarts with the same keyset store and master key "mk-1"
    And encoded payload "p1" is decoded for tenant "acme"
    Then the decoded payload data is "sk-live-4f9a2c"

  @security @REQ-SECRET-010 @id:SECRET-010-01
  Scenario: Two codecs encoding a tenant's first payload at once agree on one keyset
    Given a second payload codec sharing the keyset store and master key "mk-1"
    When both payload codecs encode a first payload for tenant "acme" at the same moment
    Then the keyset store holds 1 keyset for tenant "acme"
    And each payload codec decodes for tenant "acme" the payload the other one encoded

  @security @REQ-SECRET-011 @id:SECRET-011-01
  Scenario: After a rotation the tenant's new payloads use a new data key
    Given payload "p1" with encoding "json/plain" and data "{}" encoded for tenant "acme"
    And payload "g1" with encoding "json/plain" and data "{}" encoded for tenant "globex"
    When the data key of tenant "acme" is rotated
    And payload "p2" with encoding "json/plain" and data "{}" is encoded for tenant "acme"
    And payload "g2" with encoding "json/plain" and data "{}" is encoded for tenant "globex"
    Then encoded payloads "p1" and "p2" name different data keys
    And encoded payload "p2" names the primary data key of tenant "acme"
    And encoded payloads "g1" and "g2" name the same data key

  @security @REQ-SECRET-012 @id:SECRET-012-01
  Scenario: Payloads encoded under earlier data keys still decode
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    When the data key of tenant "acme" is rotated
    And the data key of tenant "acme" is rotated
    And encoded payload "p1" is decoded for tenant "acme"
    Then the decoded payload data is "sk-live-4f9a2c"
    And the keyset of tenant "acme" holds 3 data keys

  @security @REQ-SECRET-013 @id:SECRET-013-01
  Scenario: Rotating the master key keeps old payloads readable after the old key is gone
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And the simulated key service also holds master key "mk-2"
    When the master key is rotated from "mk-1" to "mk-2"
    And the simulated key service deletes master key "mk-1"
    And the payload codec restarts with the same keyset store and master key "mk-2"
    And encoded payload "p1" is decoded for tenant "acme"
    Then the decoded payload data is "sk-live-4f9a2c"
    And the stored keyset of tenant "acme" names master key "mk-2" as the key that wrapped it

  @security @REQ-SECRET-014 @REQ-SECRET-016 @id:SECRET-014-01
  Scenario: A master key rotation that stops halfway fails and leaves each keyset under one master key
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And payload "g1" with encoding "json/plain" and data "sk-live-77b0e1" encoded for tenant "globex"
    And the simulated key service also holds master key "mk-2"
    And the simulated key service becomes unreachable once one keyset has been rewrapped
    When the master key is rotated from "mk-1" to "mk-2"
    Then the rotation fails with code "unavailable"
    And one stored keyset names master key "mk-1" and the other names master key "mk-2"

  @security @REQ-SECRET-014 @id:SECRET-014-03
  Scenario: After an interrupted master key rotation, payloads decode with both master keys configured
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And payload "g1" with encoding "json/plain" and data "sk-live-77b0e1" encoded for tenant "globex"
    And the simulated key service also holds master key "mk-2"
    And the simulated key service becomes unreachable once one keyset has been rewrapped
    And a master key rotation from "mk-1" to "mk-2" has failed once
    And the simulated key service is reachable again
    When the payload codec restarts with the same keyset store and master keys "mk-1" and "mk-2"
    Then encoded payload "p1" decodes for tenant "acme" to "sk-live-4f9a2c"
    And encoded payload "g1" decodes for tenant "globex" to "sk-live-77b0e1"

  @security @REQ-SECRET-014 @id:SECRET-014-02
  Scenario: Running the rotation again rewraps the remaining keysets under the new master key
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And payload "g1" with encoding "json/plain" and data "sk-live-77b0e1" encoded for tenant "globex"
    And the simulated key service also holds master key "mk-2"
    And the simulated key service becomes unreachable once one keyset has been rewrapped
    And a master key rotation from "mk-1" to "mk-2" has failed once
    And the simulated key service is reachable again
    When the master key is rotated from "mk-1" to "mk-2"
    Then the stored keysets of tenants "acme" and "globex" name master key "mk-2" as the key that wrapped them

  @security @REQ-SECRET-015 @id:SECRET-015-01
  Scenario: Decoding with the wrong master key fails and leaves the stored keyset intact
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And the simulated key service also holds master key "mk-other"
    When the payload codec restarts with the same keyset store and master key "mk-other"
    And encoded payload "p1" is decoded for tenant "acme"
    Then decoding fails with code "internal" and message "keyset of the tenant cannot be unwrapped"
    And the stored keyset of tenant "acme" is unchanged

  @security @REQ-SECRET-015 @id:SECRET-015-02
  Scenario: Encoding with the wrong master key fails instead of replacing the stored keyset
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And the simulated key service also holds master key "mk-other"
    When the payload codec restarts with the same keyset store and master key "mk-other"
    And a new payload is encoded for tenant "acme"
    Then encoding fails with code "internal" and message "keyset of the tenant cannot be unwrapped"
    And the stored keyset of tenant "acme" is unchanged

  @security @REQ-SECRET-015 @id:SECRET-015-03
  Scenario: Restarting again with the right master key decodes the payload again
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And the simulated key service also holds master key "mk-other"
    And the payload codec has restarted with the same keyset store and master key "mk-other"
    And decoding encoded payload "p1" for tenant "acme" has failed
    When the payload codec restarts with the same keyset store and master key "mk-1"
    And encoded payload "p1" is decoded for tenant "acme"
    Then the decoded payload data is "sk-live-4f9a2c"

  @security @REQ-SECRET-016 @id:SECRET-016-01
  Scenario: Encoding fails while the key service is unreachable, and nothing leaves unencrypted
    Given the simulated key service is unreachable
    When payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" is encoded for tenant "acme"
    Then encoding fails with code "unavailable"
    And no encoded payload is returned
    And the keyset store holds no keyset for tenant "acme"

  @security @REQ-SECRET-016 @id:SECRET-016-02
  Scenario: Decoding after a restart fails while the key service is unreachable
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And the payload codec restarts with the same keyset store and master key "mk-1"
    And the simulated key service is unreachable
    When encoded payload "p1" is decoded for tenant "acme"
    Then decoding fails with code "unavailable"
    And no decoded payload is returned

  @REQ-SECRET-017 @id:SECRET-017-01
  Scenario: A tenant's keyset is unwrapped once however many payloads use it
    Given payload "p0" with encoding "json/plain" and data "{}" encoded for tenant "acme"
    And the payload codec restarts with the same keyset store and master key "mk-1"
    When 100 payloads are encoded and decoded for tenant "acme"
    Then the simulated key service received 1 unwrap request since the restart

  @REQ-SECRET-017 @id:SECRET-017-02
  Scenario: An unwrapped keyset keeps working while the key service is unreachable
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And the simulated key service is unreachable
    When payload "p2" with encoding "json/plain" and data "{}" is encoded for tenant "acme"
    And encoded payload "p1" is decoded for tenant "acme"
    Then the decoded payload data is "sk-live-4f9a2c"
    And encoded payload "p2" decodes for tenant "acme" to "{}"

  @security @REQ-SECRET-018 @id:SECRET-018-01
  Scenario: A codec decodes a payload under a data key another codec rotated in
    Given a second payload codec sharing the keyset store and master key "mk-1"
    And each payload codec has encoded a payload for tenant "acme"
    When the second payload codec rotates the data key of tenant "acme"
    And the second payload codec encodes payload "p2" with encoding "json/plain" and data "sk-live-4f9a2c" for tenant "acme"
    And encoded payload "p2" is decoded for tenant "acme" by the first payload codec
    Then the decoded payload data is "sk-live-4f9a2c"
    And the first payload codec sent 1 unwrap request to the simulated key service for that decode

  @security @REQ-SECRET-023 @id:SECRET-023-01
  Scenario: A swapped-in keyset wrapped for another tenant cannot be unwrapped
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And payload "g1" with encoding "json/plain" and data "sk-live-77b0e1" encoded for tenant "globex"
    And the stored keyset of tenant "acme" is replaced by the stored keyset of tenant "globex"
    When the payload codec restarts with the same keyset store and master key "mk-1"
    And encoded payload "p1" is decoded for tenant "acme"
    Then decoding fails with code "internal" and message "keyset of the tenant cannot be unwrapped"

  @security @REQ-SECRET-023 @id:SECRET-023-02
  Scenario: A keyset record in another tenant's slot decrypts neither tenant's payloads
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And payload "g1" with encoding "json/plain" and data "sk-live-77b0e1" encoded for tenant "globex"
    And the stored keyset of tenant "acme" is replaced by the stored keyset of tenant "globex"
    When the payload codec restarts with the same keyset store and master key "mk-1"
    And encoded payload "p1" is decoded for tenant "globex"
    Then decoding fails with code "internal" and message "keyset of the tenant cannot be unwrapped"
    And no decoded payload is returned
    When encoded payload "g1" is decoded for tenant "acme"
    Then decoding fails with code "internal" and message "keyset of the tenant cannot be unwrapped"

  @REQ-SECRET-024 @id:SECRET-024-01
  Scenario: A tampered payload on a cold codec unwraps the keyset once
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And the payload codec restarts with the same keyset store and master key "mk-1"
    When encoded payload "p1" is tampered with: one bit of the ciphertext is flipped
    And encoded payload "p1" is decoded for tenant "acme"
    Then decoding fails with code "invalid_argument" and message "payload cannot be decrypted"
    And the simulated key service received 1 unwrap request since the restart

  @REQ-SECRET-024 @id:SECRET-024-02
  Scenario: A tampered payload on a warm codec unwraps no keyset
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And the payload codec restarts with the same keyset store and master key "mk-1"
    And encoded payload "p1" is decoded for tenant "acme"
    When encoded payload "p1" is tampered with: one bit of the ciphertext is flipped
    And encoded payload "p1" is decoded for tenant "acme" by the first payload codec
    Then decoding fails with code "invalid_argument" and message "payload cannot be decrypted"
    And the first payload codec sent 0 unwrap request to the simulated key service for that decode

  @REQ-SECRET-024 @id:SECRET-024-03
  Scenario: A tampered payload and then 100 valid ones unwrap the keyset once in total
    Given payload "p1" with encoding "json/plain" and data "sk-live-4f9a2c" encoded for tenant "acme"
    And the payload codec restarts with the same keyset store and master key "mk-1"
    When encoded payload "p1" is tampered with: one bit of the ciphertext is flipped
    And encoded payload "p1" is decoded for tenant "acme"
    And 100 payloads are encoded and decoded for tenant "acme"
    Then decoding fails with code "invalid_argument" and message "payload cannot be decrypted"
    And the simulated key service received 1 unwrap request since the restart
