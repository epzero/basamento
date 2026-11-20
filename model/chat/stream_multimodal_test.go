package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

func TestStreamMultimodalPayloadLimitEndsInvocation(t *testing.T) {
	big := strings.Repeat("x", 32)
	url := "https://example.com/" + big
	b64 := strings.Repeat("A", 32)
	cases := []struct {
		name  string
		chunk *schema.Message
	}{
		{
			name: "assistant base64 image",
			chunk: &schema.Message{
				Role: schema.Assistant,
				AssistantGenMultiContent: []schema.MessageOutputPart{{
					Type: schema.ChatMessagePartTypeImageURL,
					Image: &schema.MessageOutputImage{
						MessagePartCommon: schema.MessagePartCommon{Base64Data: &b64},
					},
				}},
			},
		},
		{
			name: "assistant audio url",
			chunk: &schema.Message{
				Role: schema.Assistant,
				AssistantGenMultiContent: []schema.MessageOutputPart{{
					Type: schema.ChatMessagePartTypeAudioURL,
					Audio: &schema.MessageOutputAudio{
						MessagePartCommon: schema.MessagePartCommon{URL: &url},
					},
				}},
			},
		},
		{
			name: "assistant video url",
			chunk: &schema.Message{
				Role: schema.Assistant,
				AssistantGenMultiContent: []schema.MessageOutputPart{{
					Type: schema.ChatMessagePartTypeVideoURL,
					Video: &schema.MessageOutputVideo{
						MessagePartCommon: schema.MessagePartCommon{URL: &url},
					},
				}},
			},
		},
		{
			name:  "legacy multi content image url",
			chunk: legacyMultiContentImageChunk(t, url),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := adapterWithLimits(t, &fakeChat{chunks: []*schema.Message{tc.chunk}}, 16, defaultMaxStreamChunks)
			res, err := model.Invoke(testCtx(t), a, model.Request{
				Model: "sim-chat", Op: model.CapChat, ClientRef: "mm-" + tc.name,
				Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
			})
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			if res.State != model.StateFailed {
				t.Fatalf("state = %q, want failed", res.State)
			}
			var failure model.Failure
			if err := json.Unmarshal(res.Error, &failure); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if failure.Code != core.CodeVendor {
				t.Fatalf("code = %q, want vendor_error", failure.Code)
			}
			if failure.Message != "stream payload exceeded 16 bytes" {
				t.Fatalf("message = %q", failure.Message)
			}
		})
	}
}

func TestSmallMultimodalStreamSucceeds(t *testing.T) {
	short := "hi"
	a := adapterWithLimits(t, &fakeChat{chunks: []*schema.Message{{
		Role: schema.Assistant,
		AssistantGenMultiContent: []schema.MessageOutputPart{{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageOutputImage{
				MessagePartCommon: schema.MessagePartCommon{Base64Data: &short},
			},
		}},
	}}}, 64, defaultMaxStreamChunks)
	res, err := model.Invoke(testCtx(t), a, model.Request{
		Model: "sim-chat", Op: model.CapChat, ClientRef: "mm-small",
		Messages: messages(t, &schema.Message{Role: schema.User, Content: "hi"}),
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.State != model.StateSucceeded {
		t.Fatalf("state = %q, want succeeded", res.State)
	}
}

func legacyMultiContentImageChunk(t *testing.T, imageURL string) *schema.Message {
	t.Helper()
	raw := fmt.Sprintf(`{"role":"assistant","multi_content":[{"type":"image_url","image_url":{"url":%q}}]}`, imageURL)
	var msg schema.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("decode legacy chunk: %v", err)
	}
	return &msg
}
