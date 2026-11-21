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
// (REQ-AGENT-023, REQ-AGENT-024, REQ-AGENT-025). A weight that was not
// declared or reported is estimated from its content, so an unweighed message
// is never free; the kept set is measured once the messages are assembled, and
// one that exceeds limit fails the run (REQ-AGENT-027). The second result is
// the number of messages dropped; limit <= 0 means no limit.
func buildMessages(system []SystemMessage, turns []Turn, limit int64) ([]*schema.Message, int, error) {
	sysTokens := int64(0)
	for _, m := range system {
		sysTokens += systemTokens(m)
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

// systemTokens is a system message's weight: its declared count, or an
// estimate from its content when it declares none. Keeping a system message
// with no count out of the limit would let a large prompt stay free.
func systemTokens(m SystemMessage) int64 {
	if m.Tokens > 0 {
		return m.Tokens
	}
	return contentTokens(m.Content)
}

// turnTokens is a turn's weight for the memory policy: the usage the model
// reported for it plus the tool-result messages it produced. A turn with no
// reported usage is estimated from its reply instead of weighing nothing.
func turnTokens(t Turn) int64 {
	total := t.Tokens
	if total == 0 {
		total = assistantTokens(t.Message)
	}
	for _, raw := range t.Results {
		total += toolResultTokens(raw)
	}
	return total
}

// assistantTokens estimates a recorded model turn's weight when its step
// reported no usage. It weighs every string the assistant produced - text,
// reasoning, the parts of legacy multi-content and assistant-generated
// content, and each tool call's id, type, name and arguments - at four bytes
// per token, rounded up, plus one token for the message envelope. The string
// set mirrors the chat adapter's payload walk (model/chat/output.go
// foreachAssistantOutputString, tool calls included); the loop does not import
// the adapter, so the two walks are kept in step by hand. A turn that puts its
// bulk in reasoning or a multi-content part must not weigh as if it were
// empty.
func assistantTokens(raw json.RawMessage) int64 {
	var m schema.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return (int64(len(raw)) + 3) / 4
	}
	bytes := 0
	forEachAssistantString(&m, func(s string) { bytes += len(s) })
	return 1 + (int64(bytes)+3)/4
}

// forEachAssistantString calls fn for every string of an assistant message
// that travels back to the model. It mirrors foreachAssistantOutputString in
// model/chat/output.go with tool calls included, without depending on the chat
// adapter package.
func forEachAssistantString(m *schema.Message, fn func(string)) {
	fn(m.Content)
	fn(m.ReasoningContent)
	for i := range m.ToolCalls {
		call := &m.ToolCalls[i]
		fn(call.ID)
		fn(call.Type)
		fn(call.Function.Name)
		fn(call.Function.Arguments)
	}
	// MultiContent is deprecated in eino but ConcatMessages still merges it.
	for i := range m.MultiContent {
		part := &m.MultiContent[i]
		fn(part.Text)
		if part.ImageURL != nil {
			fn(part.ImageURL.URL)
			fn(part.ImageURL.URI)
		}
		if part.AudioURL != nil {
			fn(part.AudioURL.URL)
			fn(part.AudioURL.URI)
		}
		if part.VideoURL != nil {
			fn(part.VideoURL.URL)
			fn(part.VideoURL.URI)
		}
		if part.FileURL != nil {
			fn(part.FileURL.URL)
			fn(part.FileURL.URI)
		}
	}
	for i := range m.AssistantGenMultiContent {
		part := &m.AssistantGenMultiContent[i]
		fn(part.Text)
		if part.Reasoning != nil {
			fn(part.Reasoning.Text)
		}
		if part.Image != nil {
			appendMessagePartCommon(part.Image.MessagePartCommon, fn)
		}
		if part.Audio != nil {
			appendMessagePartCommon(part.Audio.MessagePartCommon, fn)
		}
		if part.Video != nil {
			appendMessagePartCommon(part.Video.MessagePartCommon, fn)
		}
	}
}

// appendMessagePartCommon passes the string fields of a media part to fn.
func appendMessagePartCommon(c schema.MessagePartCommon, fn func(string)) {
	if c.URL != nil {
		fn(*c.URL)
	}
	if c.Base64Data != nil {
		fn(*c.Base64Data)
	}
	if c.FileID != nil {
		fn(*c.FileID)
	}
	fn(c.MIMEType)
}

// contentTokens estimates a message's input tokens at four bytes per token,
// rounded up, plus one token for the message envelope.
func contentTokens(text string) int64 {
	return 1 + (int64(len(text))+3)/4
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
	return contentTokens(m.Content)
}
