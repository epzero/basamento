// Package prompt holds versioned prompt templates (id@version, declared
// inputs, strict {{var}} substitution), segment assembly with size budgets and
// cache hints (static, semi-static, dynamic, history, user), untrusted-input
// wrapping that neutralizes delimiter break-out, and assembled-prompt linting.
//
// Each history entry role is a short ASCII token matching
// ^[A-Za-z][A-Za-z0-9_-]{0,31}$ (for example user or assistant). The
// assembler rejects any other role with invalid_argument and does not echo
// the rejected role. Roles are written before wrapped entry text in model-facing
// output, so they are validated; wrapped text follows REQ-PROMPT-014 and
// REQ-PROMPT-015.
//
// WrapUntrusted restricts the source attribute to [A-Za-z0-9_.-]; every other
// rune is replaced with '_' before the opening delimiter is written.
package prompt
