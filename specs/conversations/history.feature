@planned @wp:WP-L7-03 @runtime:any @level:component
Feature: The history a new turn sees

  Background:
    Given tenant "acme" has a conversation "c1" of user "ana" on an "assistant" flow

  @REQ-CONV-012 @id:CONV-012-01
  Scenario: A new turn sees the completed turns, in order, then its own message
    Given ana sent "Hello" and the turn completed with the model message "Hi Ana"
    And ana sent "What is a run?" and the turn completed with the model message "A unit of execution"
    When ana sends "And a flow?"
    Then the scripted model's call for the third turn records these texts in order:
      | Hello                |
      | Hi Ana               |
      | What is a run?       |
      | A unit of execution  |
      | And a flow?          |

  @REQ-CONV-012 @id:CONV-012-02
  Scenario Outline: A turn leaves out what is not a completed turn
    Given ana sent "Hello" and the turn completed with the model message "Hi Ana"
    And ana sent "<message>" and the turn ended with state "<state>"
    When ana sends "What next?"
    Then the scripted model's call for the third turn records these texts in order:
      | Hello      |
      | Hi Ana     |
      | What next? |

    Examples:
      | message              | state     |
      | Write 100 paragraphs | cancelled |
      | Summarise the mail   | failed    |

  @REQ-CONV-012 @id:CONV-012-03
  Scenario: The message that starts a turn is in the history its steering messages extend
    Given ana sent "Hello" and the turn completed with the model message "Hi Ana"
    And ana sends "Write a poem"
    And the scripted model starts writing "Roses are" as the first model message
    When the turn finishes its first step
    And ana steers "About the sea"
    And the turn completes
    Then the scripted model's second call records these texts in order:
      | Hello      |
      | Hi Ana     |
      | Write a poem |
      | Roses are  |
      | About the sea |
