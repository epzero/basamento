package chat

import (
	"github.com/cloudwego/eino/schema"
)

// chunkPayloadBytes counts bytes one stream chunk contributes toward the
// adapter's payload cap. Extra and ResponseMeta are not counted: they carry
// vendor metadata (request ids, finish reasons, logprobs), not model output.
func chunkPayloadBytes(chunk *schema.Message) int {
	if chunk == nil {
		return 0
	}
	var n int
	foreachAssistantOutputString(chunk, func(s string) { n += len(s) })
	return n
}

// messageHasAssistantOutput reports whether m carries usable model output after
// chunks are concatenated: plain text, reasoning, tool calls, or assistant
// multi-content parts. Extra and ResponseMeta alone are not output; a vendor
// refusal or content-filter signal must appear as text or a typed error from
// the provider adapter before the message reaches this check.
func messageHasAssistantOutput(m *schema.Message) bool {
	if m == nil {
		return false
	}
	if len(m.ToolCalls) > 0 {
		return true
	}
	has := false
	foreachAssistantOutputString(m, func(s string) {
		if s != "" {
			has = true
		}
	})
	return has
}

func foreachAssistantOutputString(m *schema.Message, fn func(string)) {
	fn(m.Content)
	fn(m.ReasoningContent)
	for i := range m.ToolCalls {
		tc := &m.ToolCalls[i]
		fn(tc.ID)
		fn(tc.Type)
		fn(tc.Function.Name)
		fn(tc.Function.Arguments)
	}
	// MultiContent is deprecated in eino but ConcatMessages still merges it.
	for i := range m.MultiContent {
		partMultiContentString(&m.MultiContent[i], fn)
	}
	for i := range m.AssistantGenMultiContent {
		partAssistantOutputString(&m.AssistantGenMultiContent[i], fn)
	}
}

func partMultiContentString(p *schema.ChatMessagePart, fn func(string)) {
	fn(p.Text)
	if p.ImageURL != nil {
		fn(p.ImageURL.URL)
		fn(p.ImageURL.URI)
	}
	if p.AudioURL != nil {
		fn(p.AudioURL.URL)
		fn(p.AudioURL.URI)
	}
	if p.VideoURL != nil {
		fn(p.VideoURL.URL)
		fn(p.VideoURL.URI)
	}
	if p.FileURL != nil {
		fn(p.FileURL.URL)
		fn(p.FileURL.URI)
	}
}

func partAssistantOutputString(p *schema.MessageOutputPart, fn func(string)) {
	fn(p.Text)
	if p.Reasoning != nil {
		fn(p.Reasoning.Text)
	}
	if p.Image != nil {
		addMessagePartCommonBytes(p.Image.MessagePartCommon, fn)
	}
	if p.Audio != nil {
		addMessagePartCommonBytes(p.Audio.MessagePartCommon, fn)
	}
	if p.Video != nil {
		addMessagePartCommonBytes(p.Video.MessagePartCommon, fn)
	}
}

func addMessagePartCommonBytes(c schema.MessagePartCommon, fn func(string)) {
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
