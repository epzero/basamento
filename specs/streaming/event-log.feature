@planned @wp:WP-L6-01 @level:component
Feature: Durable event log and relay
  Every durable event of a run is written with the state change that produced
  it, numbered per run, and relayed to the bus at least once in order.

  Background:
    Given a tenant "acme" with a durable event log

  @REQ-STRM-008 @id:STRM-008-01 @contract
  Scenario: A run's events are numbered from 1 without a gap
    Given a run "r1" of tenant "acme" in state "running"
    When the state of "r1" changes to "running" and records events "node.started", "gate.opened"
    Then the log of "r1" holds events with sequences 1 to 2 and types ["node.started", "gate.opened"]

  @REQ-STRM-008 @REQ-STRM-009 @id:STRM-009-01
  Scenario: Concurrent state changes get distinct sequences
    Given a run "r1" of tenant "acme" in state "running"
    And a run "r2" of tenant "acme" in state "running"
    When 20 state changes of "r1" and 20 state changes of "r2" are recorded at the same time, each recording "node.started"
    Then the log of "r1" holds events with sequences 1 to 20 and types ["node.started" repeated 20 times]
    And the log of "r2" holds events with sequences 1 to 20 and types ["node.started" repeated 20 times]

  @REQ-STRM-010 @id:STRM-010-01 @contract
  Scenario Outline: A crash while appending leaves a change whole or not at all
    Given a run "r1" of tenant "acme" in state "running"
    When the process stops at "<point>" during a state change that records 2 events
    Then the log of "r1" holds either no events and state "running" or events with sequences 1 to 2 and state "completed"

    Examples:
      | point                        |
      | before any event is written  |
      | the first event written      |
      | the second event written     |
      | the state change written     |

  @REQ-STRM-010 @REQ-STRM-011 @id:STRM-011-01
  Scenario: A committed change carries all of its events
    Given a run "r1" of tenant "acme" in state "running"
    When the state of "r1" changes to "completed" and records events "node.completed", "run.completed"
    Then the state of "r1" in the log is "completed"
    And the log of "r1" holds events with sequences 1 to 2 and types ["node.completed", "run.completed"]

  @REQ-STRM-012 @id:STRM-012-01
  Scenario: A rejected change leaves no trace
    Given a run "r1" of tenant "acme" in state "running"
    When the state of "r1" changes to "completed" and records an event whose write is rejected
    Then the write fails with code "conflict"
    And the state of "r1" in the log is "running"
    And the log of "r1" holds no events

  @REQ-STRM-013 @id:STRM-013-01
  Scenario: The relay publishes in sequence order
    Given the log of "r1" holds events with sequences 1 to 3 and types ["run.started", "node.started", "run.completed"]
    When the relay runs and the bus accepts
    Then bus "acme" received from "r1" sequences [1, 2, 3] in this order

  @REQ-STRM-014 @id:STRM-014-01
  Scenario: Sent entries are marked relayed
    Given the log of "r1" holds events with sequences 1 to 3 and types ["run.started", "node.started", "run.completed"]
    When the relay runs and the bus accepts
    Then outbox entries 1 to 3 of "r1" are marked relayed
    And a second relay run publishes nothing to "acme"

  @REQ-STRM-015 @id:STRM-015-01
  Scenario Outline: A relay that stops before marking publishes again on restart
    Given the log of "r1" holds events with sequences 1 to 3 and types ["run.started", "node.started", "run.completed"]
    When the relay stops at "<point>"
    And the relay restarts and the bus accepts
    Then outbox entries 1 to 3 of "r1" are marked relayed
    And bus "acme" received from "r1" sequences [1, 2, 3, 3] in this order

    Examples:
      | point                                 |
      | after sending entry 3, before marking |
      | while marking entry 3, before saving  |

  @REQ-STRM-016 @id:STRM-016-01 @contract
  Scenario: A consumer tells a repeated event by run and sequence
    Given the log of "r1" holds events with sequences 1 to 1 and types ["run.started"]
    When the relay stops at "after sending entry 1, before marking"
    And the relay restarts and the bus accepts
    Then bus "acme" delivered 2 copies of the event "r1" with sequence 1
    And the 2 copies carry the same run id, sequence, event type, schema version and data

  @REQ-STRM-017 @id:STRM-017-01
  Scenario: An unreachable bus delays the relay but not the run
    Given "r1" is in state "running" and the bus is unreachable
    When the state of "r1" changes to "completed" and records events "node.completed", "run.completed"
    Then the state of "r1" in the log is "completed"
    And the log of "r1" holds events with sequences 1 to 2 and types ["node.completed", "run.completed"]
    And bus "acme" received no event from "r1"

  @REQ-STRM-018 @id:STRM-018-01
  Scenario: An unreachable bus holds the run's next entries
    Given the log of "r1" holds events with sequences 1 to 3 and types ["run.started", "node.started", "run.completed"]
    And the bus is unreachable after accepting 2 publishes
    When the relay runs
    Then outbox entries 1 to 2 of "r1" are marked relayed and entry 3 is not
    And bus "acme" received from "r1" sequences [1, 2] in this order

  @REQ-STRM-019 @id:STRM-019-01
  Scenario: A bus that comes back receives what was held
    Given the log of "r1" holds events with sequences 1 to 3 and types ["run.started", "node.started", "run.completed"]
    And the bus is unreachable after accepting 1 publish
    And the relay has run
    When the bus becomes reachable
    And the relay runs
    Then bus "acme" received from "r1" sequences [1, 2, 3] in this order

  @REQ-STRM-020 @id:STRM-020-01
  Scenario: A read after a cursor returns the rest in order
    Given the log of "r1" holds events with sequences 1 to 4 and types ["run.started", "node.started", "gate.opened", "run.completed"]
    When the principal of "acme" reads "r1" after sequence 1
    Then the read returns events with sequences 2 to 4 and types ["node.started", "gate.opened", "run.completed"]

  @REQ-STRM-020 @id:STRM-020-02
  Scenario: A read while concurrent writes happen returns a contiguous prefix
    Given a run "r1" of tenant "acme" in state "running"
    And a run "r2" of tenant "acme" in state "running"
    And the log of "r1" holds events with sequences 1 to 3 and types ["run.started", "node.started", "node.completed"]
    When 20 state changes of "r1" and 20 state changes of "r2" are recorded at the same time, each recording "node.started"
    And the principal of "acme" reads "r1" after sequence 3 while they are recorded, then reads again after they finish from the last sequence returned
    Then the two reads together return events of "r1" with sequences 4 to 23 in order with no gap and no duplicate

  @REQ-STRM-021 @id:STRM-021-01
  Scenario: A read at the end of a run returns nothing
    Given the log of "r1" holds events with sequences 1 to 3 and types ["run.started", "node.started", "run.completed"]
    When the principal of "acme" reads "r1" after sequence 3
    Then the read returns no events

  @REQ-STRM-021 @id:STRM-021-02
  Scenario: A read past the end of a run returns nothing
    Given the log of "r1" holds events with sequences 1 to 3 and types ["run.started", "node.started", "run.completed"]
    When the principal of "acme" reads "r1" after sequence 4
    Then the read returns no events

  @REQ-STRM-022 @security @id:STRM-022-01
  Scenario: A principal of another tenant cannot read a run's events
    Given a tenant "globex" with a durable event log
    And the log of "r1" holds events with sequences 1 to 3 and types ["run.started", "node.started", "run.completed"]
    When the principal of "globex" reads "r1" after sequence 1
    Then the read fails with code "not_found"

  @REQ-STRM-024 @id:STRM-024-01 @contract
  Scenario: The published envelope carries the run, sequence, type, version and data
    Given the log of "r1" holds events with sequences 1 to 1 and types ["run.started"]
    When the relay runs and the bus accepts
    Then the event of "r1" with sequence 1 on the bus has fields [run_id "r1", seq 1, type "run.started", v 1, data present]

  @REQ-STRM-025 @security @id:STRM-025-01
  Scenario: A read without a tenant is rejected
    Given the log of "r1" holds events with sequences 1 to 3 and types ["run.started", "node.started", "run.completed"]
    When a caller without a tenant reads "r1" after sequence 1
    Then the read fails with code "invalid_argument"
