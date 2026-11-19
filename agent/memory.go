package agent

import (
	"encoding/json"

	"github.com/cloudwego/eino/schema"
)

// buildMessages builds the messages of a model turn: the agent's system
// messages, then each kept model turn with the tool results that followed it
// (specs/agents/design.md §5). When the history exceeds limit, it keeps the
// system messages and the first model turn whole, then the most recent whole
// turns that fit in the remainder, latest first, dropping whole turns
// (REQ-AGENT-023, REQ-AGENT-024, REQ-AGENT-025). A kept set that still
// exceeds limit is REQ-AGENT-027's failure. The second result is the number of
// messages dropped; limit <= 0 means no limit.
func buildMessages(system []SystemMessage, turns []Turn, limit int64) ([]*schema.Message, int, error) {
	sysTokens := int64(0)
	for _, m := range system {
		sysTokens += m.Tokens
	}
	total := sysTokens
	for _, t := range turns {
		total += t.Tokens
	}

	kept := make([]bool, len(turns))
	for i := range kept {
		kept[i] = true
	}
	dropped := 0
	if limit > 0 && total > limit {
		for i := range kept {
			kept[i] = false
		}
		if sysTokens > limit {
			return nil, 0, invalid("agent: system messages exceed the input token limit", map[string]any{
				"input_tokens": limit, "used": sysTokens,
			})
		}
		remainder := limit - sysTokens
		start := 0
		if len(turns) > 0 {
			if turns[0].Tokens > remainder {
				return nil, 0, invalid("agent: the first model turn exceeds the input token limit", map[string]any{
					"input_tokens": limit, "used": sysTokens + turns[0].Tokens,
				})
			}
			kept[0] = true
			remainder -= turns[0].Tokens
			start = 1
		}
		for i := len(turns) - 1; i >= start; i-- {
			if turns[i].Tokens > remainder {
				break
			}
			kept[i] = true
			remainder -= turns[i].Tokens
		}
		for i := start; i < len(turns); i++ {
			if !kept[i] {
				dropped += 1 + len(turns[i].Results)
			}
		}
	}

	msgs := make([]*schema.Message, 0, len(system)+len(turns))
	for _, m := range system {
		msgs = append(msgs, &schema.Message{Role: schema.System, Content: m.Content})
	}
	for i, turn := range turns {
		if !kept[i] {
			continue
		}
		var msg schema.Message
		if err := json.Unmarshal(turn.Message, &msg); err != nil {
			return nil, 0, invalid("agent: recorded model turn is not a message: "+err.Error(), nil)
		}
		msgs = append(msgs, &msg)
		for _, raw := range turn.Results {
			var result schema.Message
			if err := json.Unmarshal(raw, &result); err != nil {
				return nil, 0, invalid("agent: recorded tool result is not a message: "+err.Error(), nil)
			}
			msgs = append(msgs, &result)
		}
	}
	return msgs, dropped, nil
}
