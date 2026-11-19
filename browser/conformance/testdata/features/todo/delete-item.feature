Feature: Delete an item from the todo app

  Scenario: ana deletes an item
    Given ana opens the todo app with one item
    When ana deletes the item
    Then the list is empty
