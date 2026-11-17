@planned @wp:WP-L7-02 @level:component
Feature: Tools found on an MCP server
  An agent reaches the tools of an MCP server by describing that server. The
  server's tools appear as if they were local, a call is forwarded and its
  result comes back, and the server's credentials stay where the vault keeps
  them. Every scenario runs against a simulated MCP server.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a simulated MCP server "weather"

  @REQ-TOOL-012 @id:TOOL-012-02
  Scenario: A server's tools appear with their descriptions and schemas
    Given the simulated MCP server "weather" offers tool "forecast" described as "Forecasts rain for a city", requiring "city", and tool "alerts" described as "Lists active alerts", requiring "region"
    When agent "assistant" uses the MCP server "weather"
    Then agent "assistant" is offered the tools "mcp_weather__forecast" and "mcp_weather__alerts"
    And tool "mcp_weather__forecast" is described as "Forecasts rain for a city" and requires "city"
    And tool "mcp_weather__alerts" requires "region"

  @REQ-TOOL-013 @REQ-TOOL-012 @id:TOOL-013-01
  Scenario: A call is forwarded to the server and its result returned
    Given agent "assistant" uses the MCP server "weather" that offers tool "forecast" requiring "city"
    And the simulated MCP server "weather" answers "forecast" with {"rain_mm": 4}
    When agent "assistant" calls tool "mcp_weather__forecast" with arguments {"city": "Porto"}
    Then the call succeeds with the result {"rain_mm": 4}
    And the simulated MCP server "weather" received 1 call of "forecast" with arguments {"city": "Porto"}

  @REQ-TOOL-012 @REQ-TOOL-002 @id:TOOL-012-01
  Scenario: Arguments are checked against the server's schema before forwarding
    Given agent "assistant" uses the MCP server "weather" that offers tool "forecast" requiring "city" of type "string"
    When agent "assistant" calls tool "mcp_weather__forecast" with arguments {"city": 7}
    Then the call ends with code "invalid_argument"
    And the simulated MCP server "weather" received 0 calls

  @REQ-TOOL-014 @id:TOOL-014-01
  Scenario: A server that is unreachable fails the call without forwarding it
    Given agent "assistant" uses the MCP server "weather" that offers tool "forecast" requiring "city"
    And the simulated MCP server "weather" goes offline
    When agent "assistant" calls tool "mcp_weather__forecast" with arguments {"city": "Porto"}
    Then the call ends with code "unavailable"
    And the simulated MCP server "weather" received 0 calls

  @REQ-TOOL-015 @id:TOOL-015-01
  Scenario: A server that drops the connection mid-call fails the call
    Given agent "assistant" uses the MCP server "weather" that offers tool "forecast" requiring "city"
    And the simulated MCP server "weather" drops the connection after receiving a call
    When agent "assistant" calls tool "mcp_weather__forecast" with arguments {"city": "Porto"}
    Then the call ends with code "unavailable"
    And the simulated MCP server "weather" received 1 call of "forecast"

  @REQ-TOOL-016 @id:TOOL-016-01
  Scenario: A server that changes its tools is listed again
    Given agent "assistant" uses the MCP server "weather" that offers tool "forecast" requiring "city"
    When the simulated MCP server "weather" replaces tool "forecast" with tool "radar" and reports its tool list changed
    And agent "assistant" starts a model turn
    Then the model is offered the tools "mcp_weather__radar"
    And the model is not offered a tool named "mcp_weather__forecast"

  @REQ-TOOL-017 @security @id:TOOL-017-01
  Scenario: The server's credential is resolved at the call and never recorded
    Given a credential "weather-key" whose secret is "sk-weather-7f3a"
    And agent "assistant" uses the MCP server "weather" that offers tool "forecast" requiring "city", with credential "weather-key"
    And the simulated MCP server "weather" answers "forecast" with {"rain_mm": 4}
    When agent "assistant" calls tool "mcp_weather__forecast" with arguments {"city": "Porto"}
    Then the simulated MCP server "weather" received the credential "sk-weather-7f3a"
    And the secret "sk-weather-7f3a" appears in no run event, history entry, error or log line

  @REQ-TOOL-024 @id:TOOL-024-01
  Scenario: A server tool whose prefixed name collides with an existing tool is rejected
    Given tool "mcp_weather__forecast" exists and is given to agent "assistant"
    And the simulated MCP server "weather" offers tool "forecast" requiring "city"
    When agent "assistant" uses the MCP server "weather"
    Then listing the tools fails with code "conflict"

  @REQ-TOOL-025 @id:TOOL-025-01
  Scenario: An error returned by the server ends the call as a vendor error
    Given agent "assistant" uses the MCP server "weather" that offers tool "forecast" requiring "city"
    And the simulated MCP server "weather" answers "forecast" with the error "station offline"
    When agent "assistant" calls tool "mcp_weather__forecast" with arguments {"city": "Porto"}
    Then the call ends with code "vendor_error" and message "station offline"
