@planned @wp:WP-L0-01 @level:component
Feature: Tenant context
  A tenant id travels in the context as the isolation boundary for data and
  spend. The context reports no tenant when it carries none, or when the id it
  carries is not a valid tenant id.

  In an Examples table a tenant-id literal is written as a Go interpreted
  string literal: the text in double quotes, where \u followed by four
  hexadecimal digits is a code point, \x followed by two hexadecimal digits is
  one raw byte, \\ is a backslash and \" is a double quote. The word absent
  stands for a context with no id at all.

  @REQ-CORE-005 @REQ-CORE-006 @security @id:CORE-006-01
  Scenario Outline: An invalid tenant id is reported as no tenant
    When a context carries the tenant-id literal <literal>
    Then the context reports no tenant
    And the reported tenant id is empty

    Examples:
      | literal              | class                       |
      | absent               | absent                      |
      | ""                   | empty                       |
      | "a/.part"            | slash                       |
      | "a\\b"               | backslash                   |
      | "."                  | single dot                  |
      | ".."                 | double dot                  |
      | "\xFF\xFE"          | invalid UTF-8               |
      | "a\u0000b"           | NUL                         |
      | "a\u000Ab"           | control U+000A              |
      | "a\u001Fb"           | control U+001F              |
      | "a\u007Fb"           | DEL U+007F                  |
      | " acme"              | leading whitespace          |
      | "acme "              | trailing whitespace         |
      | "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" | 129 bytes |

  @REQ-CORE-005 @security @id:CORE-005-01
  Scenario: A short tenant id is reported unchanged
    When a context carries the tenant id "acme"
    Then the context reports the tenant id "acme"

  @REQ-CORE-005 @security @id:CORE-005-02
  Scenario: A tenant id of 128 bytes is reported unchanged
    When a context carries the tenant-id literal "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    Then the context reports the tenant id "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

  @REQ-CORE-005 @security @id:CORE-005-03
  Scenario: A non-ASCII tenant id is reported unchanged
    When a context carries the tenant id "acme-東京"
    Then the context reports the tenant id "acme-東京"
