@wp:WP-L6-02 @runtime:any @level:component
Feature: Retries and cancellation of streaming steps

  @REQ-STRM-004 @id:STRM-004-01
  Scenario: A retried streaming step resets the partial message
    Given flow "assistant" uses a scripted model that fails after 3 deltas on the first attempt and then streams "Done." in 2 deltas
    And user "ana" starts a run of "assistant"
    When the run completes
    Then ana receives a reset for the message before the deltas of attempt 2
    And the completed message text is "Done."

  @REQ-STRM-005 @id:STRM-005-01
  Scenario: Cancelling stops the model and keeps the partial text
    Given flow "assistant" uses a scripted model that streams 100 deltas slowly
    And user "ana" starts a run of "assistant" and receives 3 deltas
    When ana cancels the run
    Then the model step stops within one heartbeat interval
    And the run state becomes "cancelled"
    And the message is recorded as interrupted with the text received so far

  @REQ-STRM-006 @id:STRM-006-01
  Scenario: A detach policy cancels an unattended run
    Given flow "assistant" declares on_detach cancel_after "30m"
    And flow "assistant" uses a scripted model that streams 100 deltas slowly
    And user "ana" starts a run of "assistant" and receives 1 delta
    When ana detaches
    And 30 minutes pass with no client attached
    Then the run state becomes "cancelled"
