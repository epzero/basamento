@planned @wp:WP-L3-04 @runtime:any @level:component
Feature: Invocation outputs and model routing

  @REQ-MODEL-008 @id:MODEL-008-01
  Scenario: Output files are stored with provenance
    Given a simulated image provider that returns 2 images
    When a run invokes "image.generate" with seed 42
    Then 2 assets are stored by content hash
    And each asset records the model, the parameter hash, seed 42 and the cost

  @REQ-MODEL-011 @profile:cloud @id:MODEL-011-01
  Scenario: The cloud profile never routes to a machine-local model
    Given models "local-image" with locality "machine_local" and "cloud-image" with locality "cloud"
    And both serve capability "image.generate"
    When the router picks a model for "image.generate" in profile "cloud"
    Then the router picks "cloud-image"
