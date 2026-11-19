package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/stream"
)

// AG-UI event types the adapter emits (docs/architecture/streaming.md §7).
const (
	aguiRunStarted    = "RUN_STARTED"
	aguiRunFinished   = "RUN_FINISHED"
	aguiRunError      = "RUN_ERROR"
	aguiStepStarted   = "STEP_STARTED"
	aguiStepFinished  = "STEP_FINISHED"
	aguiTextStart     = "TEXT_MESSAGE_START"
	aguiTextContent   = "TEXT_MESSAGE_CONTENT"
	aguiTextEnd       = "TEXT_MESSAGE_END"
	aguiToolStart     = "TOOL_CALL_START"
	aguiToolArgs      = "TOOL_CALL_ARGS"
	aguiToolEnd       = "TOOL_CALL_END"
	aguiToolResult    = "TOOL_CALL_RESULT"
	aguiMessagesSnap  = "MESSAGES_SNAPSHOT"
	aguiActivityDelta = "ACTIVITY_DELTA"
	aguiCustom        = "CUSTOM"
)

// attachAGUI serves GET /agui?run_id=... over SSE. It is an adapter over the
// same attachment as the native stream: the event types are AG-UI's, the SSE
// id stays our cursor, which is what lets a client reattach (ADR-0005).
func (s *Server) attachAGUI(w http.ResponseWriter, r *http.Request) {
	if s.opts.Streams == nil {
		writeError(w, core.NewError(core.CodeUnsupported, "api: run streaming is not enabled", nil))
		return
	}
	ctx := r.Context()
	if _, err := requestTenant(ctx); err != nil {
		writeError(w, err)
		return
	}
	runID := r.URL.Query().Get("run_id")
	if runID == "" {
		writeError(w, core.NewError(core.CodeInvalidArgument, "api: run_id is required", nil))
		return
	}
	cursor, err := parseCursor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	attachment, err := s.opts.Streams.Attach(ctx, runID, cursor)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = attachment.Close() }()

	sse, ok := newSSE(w)
	if !ok {
		writeError(w, core.NewError(core.CodeUnsupported, "api: the response cannot stream", nil))
		return
	}
	pump(ctx, sse, attachment, aguiEvent)
}

// aguiEvent maps one native frame to an AG-UI event. Events AG-UI has no
// equivalent for (audio, reset, an opening gate) travel as CUSTOM, which is
// AG-UI's extension point, instead of being dropped.
func aguiEvent(frame stream.Frame, data []byte) (string, []byte, bool) {
	var event map[string]any
	switch {
	case frame.Durable != nil:
		event = durableToAGUI(frame.Durable.Type, frame.Durable.Data)
	case frame.Snapshot != nil:
		event = map[string]any{
			"type": aguiMessagesSnap,
			"messages": []map[string]any{{
				"id":      frame.Snapshot.MessageID,
				"role":    "assistant",
				"content": frame.Snapshot.Text,
			}},
		}
	case frame.Delta != nil:
		event = deltaToAGUI(*frame.Delta)
	default:
		return "", nil, false
	}
	body, err := json.Marshal(event)
	if err != nil {
		body = []byte(`{"type":"CUSTOM","name":"encode_error"}`)
		return aguiCustom, body, true
	}
	name, _ := event["type"].(string)
	if name == "" {
		name = aguiCustom
	}
	return name, body, true
}

// durableToAGUI translates a durable event by type. The payload keys are read
// from the event data because the platform's durable events carry their fields
// there, not in the event type.
func durableToAGUI(typ string, raw json.RawMessage) map[string]any {
	data := decodeData(raw)
	switch {
	case typ == "run.started":
		return map[string]any{"type": aguiRunStarted, "runId": stringField(data, "run_id")}
	case typ == "run.completed":
		return map[string]any{"type": aguiRunFinished, "runId": stringField(data, "run_id")}
	case typ == "run.cancelled" || typ == "run.terminated":
		return map[string]any{"type": aguiRunFinished, "runId": stringField(data, "run_id")}
	case typ == "run.failed":
		return map[string]any{"type": aguiRunError, "runId": stringField(data, "run_id"), "message": stringField(data, "error")}
	case typ == "node.started":
		return map[string]any{"type": aguiStepStarted, "stepName": nodeField(data)}
	case typ == "node.completed":
		return map[string]any{"type": aguiStepFinished, "stepName": nodeField(data)}
	case typ == "message.started":
		return map[string]any{"type": aguiTextStart, "messageId": stringField(data, "message_id")}
	case typ == "message.completed":
		return map[string]any{"type": aguiTextEnd, "messageId": stringField(data, "message_id")}
	case strings.HasPrefix(typ, "tool."):
		return toolToAGUI(strings.TrimPrefix(typ, "tool."), data)
	}
	return map[string]any{"type": aguiCustom, "name": typ, "value": data}
}

func toolToAGUI(suffix string, data map[string]any) map[string]any {
	switch {
	case strings.HasPrefix(suffix, "started"):
		return map[string]any{"type": aguiToolStart, "toolCallId": stringField(data, "tool_call_id"), "toolName": stringField(data, "name")}
	case strings.HasPrefix(suffix, "args") || strings.HasPrefix(suffix, "delta"):
		return map[string]any{"type": aguiToolArgs, "toolCallId": stringField(data, "tool_call_id"), "delta": stringField(data, "args")}
	case strings.HasPrefix(suffix, "result"):
		return map[string]any{"type": aguiToolResult, "toolCallId": stringField(data, "tool_call_id"), "content": stringField(data, "result")}
	case strings.HasPrefix(suffix, "completed") || strings.HasPrefix(suffix, "failed"):
		return map[string]any{"type": aguiToolEnd, "toolCallId": stringField(data, "tool_call_id")}
	}
	return map[string]any{"type": aguiCustom, "name": "tool." + suffix, "value": data}
}

// deltaToAGUI maps an ephemeral delta. Text is a text-message increment;
// progress is an activity increment; audio and a reset have no AG-UI event and
// travel as CUSTOM.
func deltaToAGUI(d stream.Delta) map[string]any {
	switch d.Kind {
	case "text":
		return map[string]any{"type": aguiTextContent, "messageId": d.MessageID, "delta": d.Text}
	case "progress":
		return map[string]any{
			"type":         aguiActivityDelta,
			"messageId":    d.MessageID,
			"activityType": "progress",
			"patch":        []map[string]any{{"op": "add", "path": "/progress", "value": d.Progress}},
		}
	case "reset":
		return map[string]any{"type": aguiCustom, "name": "reset", "value": map[string]any{"messageId": d.MessageID, "attempt": d.Attempt}}
	}
	return map[string]any{"type": aguiCustom, "name": d.Kind, "value": d}
}

func decodeData(raw json.RawMessage) map[string]any {
	data := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &data)
	}
	return data
}

func stringField(data map[string]any, key string) string {
	s, _ := data[key].(string)
	return s
}

func nodeField(data map[string]any) string {
	for _, key := range []string{"node", "node_id", "name"} {
		if s := stringField(data, key); s != "" {
			return s
		}
	}
	return ""
}
