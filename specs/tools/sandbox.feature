@planned @wp:WP-L7-02 @level:component
Feature: Running a command in a sandbox
  A flow runs a command, such as generated test code, in a sandbox: a
  simulated one in the gate, an isolated one in deployment. The command cannot
  reach the host's files, environment or secrets, and its time and output are
  bounded. What the caller sees when a limit is hit is the same everywhere.

  Background:
    Given the clock is frozen at "2026-10-01T09:00:00Z"
    And a simulated sandbox "tests"
    And agent "assistant" is given tool "run-tests", which runs a command in sandbox "tests"

  @REQ-TOOL-018 @security @id:TOOL-018-01
  Scenario: A command sees none of the host's secrets or files
    Given sandbox "tests" has no network access, so its client times out after 30 seconds
    When agent "assistant" calls tool "run-tests" with the command "printenv; cat /home/kit/vault.db; curl https://169.254.169.254/latest/meta-data/"
    Then the sandbox "tests" received 0 environment variables and no host file path
    And the call ends with code "timeout"
    And the output holds no secret

  @REQ-TOOL-018 @security @id:TOOL-018-02
  Scenario: A command reaches only the network its sandbox was granted
    Given sandbox "tests" is granted network access to "api.example.com"
    And sandbox "tests" answers the command "echo $HOME" with "exit status 0" and stdout "/home/sandbox"
    When agent "assistant" calls tool "run-tests" with the command "echo $HOME"
    Then the sandbox "tests" received the network grant "api.example.com" and nothing else
    And the call succeeds
    And the caller cannot tell from the result that the network was granted

  @REQ-TOOL-019 @id:TOOL-019-01
  Scenario: A command that exceeds the sandbox's time limit is stopped
    Given sandbox "tests" has a time limit of 60 seconds
    And sandbox "tests" runs the command "make test" until it is stopped
    When agent "assistant" calls tool "run-tests" with the command "make test"
    And 60 seconds pass
    Then the call ends with code "timeout" after 60 seconds of clock time
    And the sandbox "tests" stopped the command

  @REQ-TOOL-020 @id:TOOL-020-01
  Scenario: Output beyond the sandbox's limit is kept up to the limit and marked truncated
    Given sandbox "tests" has an output limit of 1 KiB
    And sandbox "tests" answers the command "seq 1 2000" with exit status 0 and 3000 bytes of output
    When agent "assistant" calls tool "run-tests" with the command "seq 1 2000"
    Then the call succeeds
    And the call's output is the first 1024 bytes
    And the call's output is marked truncated

  @REQ-TOOL-021 @id:TOOL-021-01
  Scenario: A command that exits non-zero comes back with its status and output
    Given sandbox "tests" answers the command "go test" with exit status 1 and stdout "FAIL example"
    When agent "assistant" calls tool "run-tests" with the command "go test"
    Then the call ends with exit status 1 and output "FAIL example"
    And tool "run-tests" reports an error

  @REQ-TOOL-022 @id:TOOL-022-01
  Scenario: A sandbox that cannot start fails the call as internal
    Given sandbox "tests" cannot start
    When agent "assistant" calls tool "run-tests" with the command "go test"
    Then the call ends with code "internal"
    And the sandbox "tests" received 0 commands
    And the run state is "running"
