package expr

import "encoding/json"

// Var declares one variable and its JSON Schema type in an expression environment.
type Var struct {
	Name   string
	Schema json.RawMessage
}
