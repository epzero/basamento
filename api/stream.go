package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/stream"
)

// SSE event names of the native stream. A durable event also carries the
// cursor in the SSE id; a delta carries its per-message sequence in the body.
const (
	eventDurable  = "durable"
	eventDelta    = "delta"
	eventSnapshot = "snapshot"
	eventError    = "error"
)

// cursorJSON is the Last-Event-ID form that carries both halves of the cursor,
// so one header is enough for reattach (docs/architecture/streaming.md §3).
type cursorJSON struct {
	Seq  int64            `json:"seq"`
	Open map[string]int64 `json:"open,omitempty"`
}

// attachStream serves GET /v1/runs/{id}/stream over SSE. The cursor comes from
// Last-Event-ID (an integer sequence or a cursorJSON) and from the optional
// "open" query parameter. The stream ends after a terminal run event, so a
// client does not wait on a connection that can never carry another event.
func (s *Server) attachStream(w http.ResponseWriter, r *http.Request) {
	if s.opts.Streams == nil {
		writeError(w, core.NewError(core.CodeUnsupported, "api: run streaming is not enabled", nil))
		return
	}
	ctx := r.Context()
	if _, err := requestTenant(ctx); err != nil {
		writeError(w, err)
		return
	}
	cursor, err := parseCursor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	attachment, err := s.opts.Streams.Attach(ctx, r.PathValue("id"), cursor)
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
	pump(ctx, sse, attachment, nativeEvent)
}

// nativeEvent maps a stream frame to its native SSE event.
func nativeEvent(frame stream.Frame, data []byte) (string, []byte, bool) {
	switch {
	case frame.Durable != nil:
		return eventDurable, data, true
	case frame.Snapshot != nil:
		return eventSnapshot, data, true
	case frame.Delta != nil:
		return eventDelta, data, true
	}
	return "", nil, false
}

// frameJSON marshals a frame's body.
func frameJSON(frame stream.Frame) ([]byte, error) {
	switch {
	case frame.Durable != nil:
		e := frame.Durable
		return json.Marshal(stream.DurableEvent{
			RunID: e.RunID,
			Seq:   int64(e.Seq),
			Type:  e.Type,
			V:     e.V,
			Data:  e.Data,
		})
	case frame.Snapshot != nil:
		return json.Marshal(frame.Snapshot)
	case frame.Delta != nil:
		return json.Marshal(frame.Delta)
	}
	return nil, core.NewError(core.CodeInternal, "api: empty stream frame", nil)
}

// pump writes frames until the run ends, the client detaches or the attachment
// fails. mapper may drop a frame by returning keep=false.
func pump(ctx context.Context, sse *sseWriter, attachment Attachment, mapper func(stream.Frame, []byte) (string, []byte, bool)) {
	for {
		frame, err := attachment.Next(ctx)
		if err != nil {
			if errors.Is(err, stream.ErrDetached) || errors.Is(err, io.EOF) ||
				errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			sse.errorEvent(core.CodeOf(err))
			return
		}
		data, err := frameJSON(frame)
		if err != nil {
			sse.errorEvent(core.CodeOf(err))
			return
		}
		name, body, keep := mapper(frame, data)
		if keep {
			sse.event(name, cursorID(attachment, frame), body)
		}
		if terminalFrame(frame) {
			return
		}
	}
}

// sseWriter writes the event-stream framing and flushes every event.
type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

// newSSE sets the stream headers and returns a writer. It fails when the
// response cannot be flushed, which would buffer a live stream until it ends.
func newSSE(w http.ResponseWriter) (*sseWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	return &sseWriter{w: w, f: flusher}, true
}

// event writes one event and flushes it. A write error means the client
// detached; the caller stops on the next read and the attachment's Close
// applies the detach policy.
func (s *sseWriter) event(name, id string, data []byte) {
	if id != "" {
		_, _ = fmt.Fprintf(s.w, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(s.w, "event: %s\n", name)
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", data)
	s.f.Flush()
}

// errorEvent ends a stream with the platform code of the failure.
func (s *sseWriter) errorEvent(code core.Code) {
	s.event(eventError, "", []byte(fmt.Sprintf(`{"code":%q}`, code)))
}

// cursorID encodes the attachment's position after frame for the SSE id field.
func cursorID(a Attachment, frame stream.Frame) string {
	c := a.Cursor()
	switch {
	case frame.Durable != nil && int64(frame.Durable.Seq) > c.Seq:
		c.Seq = int64(frame.Durable.Seq)
	case frame.Snapshot != nil && frame.Snapshot.DSeq > c.Open[frame.Snapshot.MessageID]:
		c.Open[frame.Snapshot.MessageID] = frame.Snapshot.DSeq
	case frame.Delta != nil && frame.Delta.DSeq > c.Open[frame.Delta.MessageID]:
		c.Open[frame.Delta.MessageID] = frame.Delta.DSeq
	}
	data, err := json.Marshal(cursorJSON{Seq: c.Seq, Open: cloneOpen(c.Open)})
	if err != nil {
		return strconv.FormatInt(c.Seq, 10)
	}
	return string(data)
}

func cloneOpen(open map[string]int64) map[string]int64 {
	if len(open) == 0 {
		return nil
	}
	out := make(map[string]int64, len(open))
	for id, dseq := range open {
		out[id] = dseq
	}
	return out
}

// parseCursor reads Last-Event-ID and the "open" query parameter. A bare
// integer is the durable sequence; a JSON object carries both halves. Negative
// positions are invalid_argument, as in the broker.
func parseCursor(r *http.Request) (stream.Cursor, error) {
	cursor := stream.Cursor{Open: map[string]int64{}}
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if strings.HasPrefix(strings.TrimSpace(raw), "{") {
			var c cursorJSON
			if err := json.Unmarshal([]byte(raw), &c); err != nil {
				return stream.Cursor{}, core.NewError(core.CodeInvalidArgument, "api: malformed Last-Event-ID", nil)
			}
			cursor.Seq = c.Seq
			for id, dseq := range c.Open {
				cursor.Open[id] = dseq
			}
		} else {
			seq, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil {
				return stream.Cursor{}, core.NewError(core.CodeInvalidArgument, "api: malformed Last-Event-ID", nil)
			}
			cursor.Seq = seq
		}
	}
	if raw := r.URL.Query().Get("open"); raw != "" {
		var open map[string]int64
		if err := json.Unmarshal([]byte(raw), &open); err != nil {
			return stream.Cursor{}, core.NewError(core.CodeInvalidArgument, "api: malformed open cursor", nil)
		}
		for id, dseq := range open {
			cursor.Open[id] = dseq
		}
	}
	if cursor.Seq < 0 {
		return stream.Cursor{}, core.NewError(core.CodeInvalidArgument, "api: negative cursor", nil)
	}
	for _, dseq := range cursor.Open {
		if dseq < 0 {
			return stream.Cursor{}, core.NewError(core.CodeInvalidArgument, "api: negative cursor", nil)
		}
	}
	if len(cursor.Open) == 0 {
		cursor.Open = nil
	}
	return cursor, nil
}

// terminalFrame reports whether frame ends the run.
func terminalFrame(frame stream.Frame) bool {
	if frame.Durable == nil {
		return false
	}
	switch frame.Durable.Type {
	case "run.completed", "run.failed", "run.cancelled", "run.terminated":
		return true
	}
	return false
}
