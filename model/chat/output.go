package chat

import (
	"github.com/cloudwego/eino/schema"
)

// chunkPayloadBytes counts bytes one stream chunk contributes toward the
// adapter's payload cap: text content, reasoning, and tool-call identity fields.
func chunkPayloadBytes(chunk *schema.Message) int {
	if chunk == nil {
		return 0
	}
	n := len(chunk.Content) + len(chunk.ReasoningContent)
	for i := range chunk.ToolCalls {
		tc := &chunk.ToolCalls[i]
		n += len(tc.ID) + len(tc.Type) + len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return n
}

// messageHasAssistantOutput reports whether m carries usable model output after
// chunks are concatenated: plain text, reasoning, tool calls, or assistant
// multi-content parts (content, reasoning_content, multi_content,
// assistant_output_multi_content fields of schema.Message).
func messageHasAssistantOutput(m *schema.Message) bool {
	if m == nil {
		return false
	}
	if m.Content != "" || m.ReasoningContent != "" {
		return true
	}
	if len(m.ToolCalls) > 0 {
		return true
	}
	for i := range m.MultiContent {
		if chatPartHasOutput(m.MultiContent[i]) {
			return true
		}
	}
	for i := range m.AssistantGenMultiContent {
		if outputPartHasOutput(m.AssistantGenMultiContent[i]) {
			return true
		}
	}
	return false
}

func chatPartHasOutput(p schema.ChatMessagePart) bool {
	if p.Text != "" {
		return true
	}
	if p.ImageURL != nil && (p.ImageURL.URL != "" || p.ImageURL.URI != "") {
		return true
	}
	if p.AudioURL != nil && p.AudioURL.URL != "" {
		return true
	}
	if p.VideoURL != nil && p.VideoURL.URL != "" {
		return true
	}
	if p.FileURL != nil && p.FileURL.URL != "" {
		return true
	}
	return false
}

func outputPartHasOutput(p schema.MessageOutputPart) bool {
	if p.Text != "" {
		return true
	}
	if p.Reasoning != nil && p.Reasoning.Text != "" {
		return true
	}
	if p.Image != nil && mediaCommonHasData(p.Image.MessagePartCommon) {
		return true
	}
	if p.Audio != nil && mediaCommonHasData(p.Audio.MessagePartCommon) {
		return true
	}
	if p.Video != nil && mediaCommonHasData(p.Video.MessagePartCommon) {
		return true
	}
	return false
}

func mediaCommonHasData(c schema.MessagePartCommon) bool {
	if c.URL != nil && *c.URL != "" {
		return true
	}
	if c.Base64Data != nil && *c.Base64Data != "" {
		return true
	}
	if c.FileID != nil && *c.FileID != "" {
		return true
	}
	return c.MIMEType != ""
}
