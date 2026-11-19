Feature: Add an item to the todo app

  Scenario: ana adds an item
    Given ana opens the todo app
    When ana types "buy milk" into the new item field
    And ana clicks add
    Then the list shows "buy milk"
