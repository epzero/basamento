// Package prompt holds versioned prompt templates (id@version, declared
// inputs, strict {{var}} substitution), segment assembly with size budgets and
// cache hints (static, semi-static, dynamic, history, user), untrusted-input
// wrapping that neutralizes delimiter break-out, and assembled-prompt linting.
package prompt
