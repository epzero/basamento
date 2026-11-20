package machine

import (
	"encoding/json"

	"github.com/epzero/basamento/flow"
)

// copySnapshot returns a deep copy of s so Apply can mutate without touching
// the caller's snapshot.
func copySnapshot(s flow.Snapshot) flow.Snapshot {
	out := flow.Snapshot{
		Seq:   s.Seq,
		Cost:  s.Cost,
		State: append(json.RawMessage(nil), s.State...),
		Input: append(json.RawMessage(nil), s.Input...),
	}
	if s.Visit != nil {
		out.Visit = make(map[string]int, len(s.Visit))
		for k, v := range s.Visit {
			out.Visit[k] = v
		}
	}
	if len(s.Steps) > 0 {
		out.Steps = make([]flow.Step, len(s.Steps))
		for i, step := range s.Steps {
			out.Steps[i] = flow.Step{
				Node:    step.Node,
				Attempt: step.Attempt,
				Done:    step.Done,
				Skip:    step.Skip,
				Input:   append(json.RawMessage(nil), step.Input...),
				Update:  append(json.RawMessage(nil), step.Update...),
			}
		}
	}
	return out
}
