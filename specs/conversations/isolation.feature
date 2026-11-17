@planned @wp:WP-L7-03 @runtime:any @level:component @security
Feature: Conversations are isolated by tenant and by user
  The tenant is the isolation boundary of data: one tenant's conversation is
  never found or changed from another tenant. Within a tenant (the embedded
  profile has one tenant), a conversation belongs to the user who owned it.

  Background:
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow
    And tenant "globex" has a conversation "c1" of user "ana" on an "assistant" flow

  @REQ-CONV-016 @id:CONV-016-01
  Scenario: A turn of one tenant's conversation runs only in that tenant
    When ana of tenant "acme" sends "Hello" to conversation "c1" and the turn completes
    Then acme's coordinator run for "c1" started a child run in tenant "acme"
    And no child run started for globex's conversation "c1"

  @REQ-CONV-016 @id:CONV-016-02
  Scenario: A tenant's turn reads only that tenant's history
    Given ana of tenant "acme" sent "Hello" and the turn completed with the model message "Hi Ana"
    And ana of tenant "globex" sent "Secret payroll question" and the turn completed with the model message "Payroll answer"
    When ana of tenant "globex" sends "What did I say first?" and the turn completes
    Then globex's scripted model call for this turn records these texts in order:
      | Secret payroll question |
      | Payroll answer         |
      | What did I say first?  |

  @REQ-CONV-016 @id:CONV-016-03
  Scenario: A turn of one user's conversation runs only for that user
    Given tenant "acme" has a conversation "c2" of user "ben" on an "assistant" flow
    When ana sends "Hello" to conversation "c1" and the turn completes
    Then the coordinator run of ana's conversation "c1" started 1 child run for user "ana"
    And ben's conversation "c2" has no child run

  @REQ-CONV-017 @id:CONV-017-01
  Scenario Outline: A conversation of another tenant is not found
    When ana of tenant "globex" <action> acme's conversation "c1"
    Then the action fails with code "not_found"
    And acme's conversation "c1" has no queued message

    Examples:
      | action           |
      | sends "Hello" to |
      | cancels          |

  @REQ-CONV-018 @id:CONV-018-01
  Scenario: Another user of the same tenant cannot send to a conversation
    When ben of tenant "acme" sends "Hello" to ana's conversation "c1"
    Then the send fails with code "not_found"
    And ana's conversation "c1" has no queued message

  @REQ-CONV-018 @id:CONV-018-02
  Scenario: Another user of the same tenant cannot cancel a conversation
    When ben of tenant "acme" cancels ana's conversation "c1"
    Then the cancel fails with code "not_found"
    And ana's conversation "c1" is still open
