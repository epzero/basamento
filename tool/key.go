package tool

import "fmt"

// Key returns the idempotency key of one tool call (REQ-TOOL-003):
//
//	tool:<run>:<node path>:<visit>:<call index>
//
// node path is the same one the model node uses (map index and enclosing
// subflows included), visit counts visits to that node or turns of the agent
// loop, and index counts the calls within that visit or model reply, in order.
// The key is stable for the run, so a resumed run returns the recorded outcome
// instead of running the tool again.
func Key(run, path string, visit, index int) string {
	return fmt.Sprintf("tool:%s:%s:%d:%d", run, path, visit, index)
}
