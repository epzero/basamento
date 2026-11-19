package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// ModelCall is one request a runner made to the scripted platform endpoint.
type ModelCall struct {
	// Method is the HTTP method.
	Method string
	// Path is the request path, e.g. "/v1/chat/completions".
	Path string
	// Authorization is the request's Authorization header.
	Authorization string
	// Session is the Basamento-Session header, if any.
	Session string
	// Body is the request body.
	Body []byte
}

// ScriptedModel is a deterministic stand-in for the platform's
// OpenAI-compatible endpoint. It records every call and answers each one with
// one browser-action tool call, or with the goal-reached content when asked.
// A call without the expected bearer token is refused, so a runner that does
// not use its session token fails rather than silently proceeding.
type ScriptedModel struct {
	server    *httptest.Server
	token     string
	reachGoal bool

	mu    sync.Mutex
	calls []ModelCall
}

// ModelPath is the path a runner calls on the platform endpoint.
const ModelPath = "/v1/chat/completions"

// NewScriptedModel starts a scripted endpoint that accepts only token.
func NewScriptedModel(token string) *ScriptedModel {
	m := &ScriptedModel{token: token}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

// Endpoint is the platform endpoint's base URL; a runner appends ModelPath.
func (m *ScriptedModel) Endpoint() string { return m.server.URL + "/v1" }

// Token is the session token the endpoint accepts.
func (m *ScriptedModel) Token() string { return m.token }

// ReachGoal makes the model declare the goal reached on every later call.
func (m *ScriptedModel) ReachGoal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reachGoal = true
}

// Calls returns the calls the runner made, in order.
func (m *ScriptedModel) Calls() []ModelCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ModelCall(nil), m.calls...)
}

// Close stops the endpoint.
func (m *ScriptedModel) Close() { m.server.Close() }

// GoalReached is the content that tells a runner to stop: the model declares
// the goal reached.
const GoalReached = "goal_reached"

// actionKind and actionTarget are the action the scripted model asks for.
const (
	scriptedActionKind   = "click"
	scriptedActionTarget = "#add"
)

func (m *ScriptedModel) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()

	m.mu.Lock()
	reach := m.reachGoal
	m.calls = append(m.calls, ModelCall{
		Method:        r.Method,
		Path:          r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
		Session:       r.Header.Get("Basamento-Session"),
		Body:          body,
	})
	m.mu.Unlock()

	if r.Method != http.MethodPost || r.URL.Path != ModelPath {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+m.token {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}

	content := ""
	if reach {
		content = GoalReached
	}
	reply := map[string]any{
		"id":     "cmpl-conformance",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
				"tool_calls": []any{map[string]any{
					"id":   "call-conformance",
					"type": "function",
					"function": map[string]any{
						"name":      "browser_action",
						"arguments": fmt.Sprintf(`{"kind":%q,"target":%q}`, scriptedActionKind, scriptedActionTarget),
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

// ModelAction is the action the scripted model asks a runner to take; it
// reads the arguments of the first tool call in a chat completion.
type ModelAction struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// ParseModelReply reads the content and the first tool call's action from a
// scripted endpoint's chat completion.
func ParseModelReply(body []byte) (content string, action ModelAction, err error) {
	var reply struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return "", ModelAction{}, fmt.Errorf("model reply: %w", err)
	}
	if len(reply.Choices) == 0 {
		return "", ModelAction{}, fmt.Errorf("model reply has no choices")
	}
	msg := reply.Choices[0].Message
	if len(msg.ToolCalls) > 0 {
		if err := json.Unmarshal([]byte(msg.ToolCalls[0].Function.Arguments), &action); err != nil {
			return "", ModelAction{}, fmt.Errorf("model tool call arguments: %w", err)
		}
	}
	return strings.TrimSpace(msg.Content), action, nil
}
