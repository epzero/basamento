@wp:WP-L1-05 @level:component
Feature: Profiles and port selection
  Each port has an embedded implementation and cloud ones; the scheme of the
  port's URL says which one a `kit` process uses (ADR-0004).

  @REQ-CONFIG-011 @REQ-CONFIG-013 @profile:embedded @id:CONFIG-011-01
  Scenario Outline: The embedded profile accepts its schemes
    Given the embedded profile with "<key>" set to "<scheme>:///data"
    When the configuration is validated
    Then validation succeeds
    And "<key>" is selected as "<implementation>"

    Examples:
      | key         | scheme        | implementation |
      | sql.url     | sqlite        | sqlite         |
      | blob.url    | file          | file           |
      | bus.url     | nats-embedded | nats-embedded  |
      | secrets.url | localsecrets  | localsecrets   |

  @REQ-CONFIG-012 @REQ-CONFIG-013 @profile:cloud @id:CONFIG-012-01
  Scenario Outline: The cloud profile accepts its schemes
    Given the cloud profile with "<key>" set to "<url>"
    When the configuration is validated
    Then validation succeeds
    And "<key>" is selected as "<implementation>"

    Examples:
      | key         | url                              | implementation |
      | sql.url     | postgres://db.example.test/kit   | postgres       |
      | blob.url    | s3://kit-buckets/assets          | s3             |
      | blob.url    | gs://kit-buckets/assets          | gcs            |
      | blob.url    | azblob://assets?account=kit      | azblob         |
      | bus.url     | nats://bus.example.test:4222     | nats           |
      | secrets.url | awskms:///arn:aws:kms:kit        | awskms         |
      | secrets.url | gcpkms://projects/kit            | gcpkms         |
      | secrets.url | hashivault://vault.example.test  | hashivault     |
