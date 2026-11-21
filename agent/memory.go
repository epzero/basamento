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
// (REQ-AGENT-023, REQ-AGENT-024, REQ-AGENT-025). A turn's weight is its
// recorded usage plus its tool-result messages, which report no usage of their
// own and are counted by toolResultTokens; the kept set is measured once the
// messages are assembled, and one that exceeds limit fails the run
// (REQ-AGENT-027). The second result is the number of messages dropped;
// limit <= 0 means no limit.
func buildMessages(system []SystemMessage, turns []Turn, limit int64) ([]*schema.Message, int, error) {
	sysTokens := int64(0)
	for _, m := range system {
		sysTokens += m.Tokens
	}
	total := sysTokens
	for _, t := range turns {
		total += turnTokens(t)
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
			if turnTokens(turns[0]) > remainder {
				return nil, 0, invalid("agent: the first model turn exceeds the input token limit", map[string]any{
					"input_tokens": limit, "used": sysTokens + turnTokens(turns[0]),
				})
			}
			kept[0] = true
			remainder -= turnTokens(turns[0])
			start = 1
		}
		for i := len(turns) - 1; i >= start; i-- {
			weight := turnTokens(turns[i])
			if weight > remainder {
				break
			}
			kept[i] = true
			remainder -= weight
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
	// The guarantee after the build: the messages this request actually
	// carries, tool results included, must fit the model's input limit or the
	// model is not invoked (REQ-AGENT-027). The policy's drop decision above
	// starts from recorded usage; this check measures the assembled set.
	if limit > 0 {
		keptTokens := sysTokens
		for i, turn := range turns {
			if kept[i] {
				keptTokens += turnTokens(turn)
			}
		}
		if keptTokens > limit {
			return nil, 0, invalid("agent: the kept messages exceed the input token limit", map[string]any{
				"input_tokens": limit, "used": keptTokens,
			})
		}
	}
	return msgs, dropped, nil
}

// turnTokens is a turn's weight for the memory policy: the usage the model
// reported for it plus the tool-result messages it produced.
func turnTokens(t Turn) int64 {
	total := t.Tokens
	for _, raw := range t.Results {
		total += toolResultTokens(raw)
	}
	return total
}

// toolResultTokens estimates a tool-result message's input tokens. A tool
// result carries no usage of its own, so it is counted at four bytes of its
// content per token, rounded up, plus one token for the message envelope. The
// estimate keeps large results from being free in the memory policy.
func toolResultTokens(raw json.RawMessage) int64 {
	var m schema.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return (int64(len(raw)) + 3) / 4
	}
	return 1 + (int64(len(m.Content))+3)/4
}
