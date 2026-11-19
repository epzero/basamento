@wp:WP-L6-02 @runtime:any @level:component
Feature: Detach from and reattach to a running chat
  A run does not depend on any connection. Clients detach and reattach with a
  cursor and see every event once, including messages still being written.

  Background:
    Given flow "assistant" uses a scripted model that streams "Hello world, how are you?" in 6 deltas

  @REQ-STRM-001 @id:STRM-001-01
  Scenario: The run completes while nobody is attached
    Given user "ana" starts a run of "assistant" and receives 2 deltas
    When ana detaches
    And the scripted model finishes
    Then the run state becomes "completed"

  @REQ-STRM-002 @id:STRM-002-01
  Scenario: Reattaching delivers missed durable events once
    Given user "ana" starts a run of "assistant" and receives 2 deltas
    When ana detaches
    And the scripted model finishes
    And ana reattaches with her last cursor
    Then ana receives each durable event after her cursor exactly once and in order
    And ana's stream contains message text "Hello world, how are you?" exactly once

  @REQ-STRM-003 @id:STRM-003-01
  Scenario: Reattaching mid-message sends a snapshot, then the rest
    Given user "ana" starts a run of "assistant" and receives 2 deltas
    When ana detaches
    And the scripted model streams 2 more deltas
    And ana reattaches with her last cursor
    And the scripted model finishes
    Then ana receives a snapshot covering the first 4 deltas
    And ana then receives deltas 5 to 6 with no gap and no duplicate

  @REQ-STRM-007 @id:STRM-007-01
  Scenario: Two viewers see the same order
    Given user "ana" starts a run of "assistant"
    And user "ben" attaches to ana's run
    When the scripted model finishes
    Then ana and ben received the same durable events in the same order
