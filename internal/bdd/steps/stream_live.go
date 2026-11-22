package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/stream"
)

func registerStreamLive(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, scn *godog.Scenario) (context.Context, error) {
		if !scenarioUsesStream(scn) {
			return ctx, nil
		}
		w := newStreamWorld()
		world(ctx).Stream = w
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Stream; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	sc.Step(`^flow "([^"]*)" uses a scripted model that streams "([^"]*)" in (\d+) deltas$`, scriptedModel)
	sc.Step(`^flow "([^"]*)" uses a scripted model that fails after (\d+) deltas on the first attempt and then streams "([^"]*)" in (\d+) deltas$`, scriptedFailure)
	sc.Step(`^flow "([^"]*)" uses a scripted model that streams (\d+) deltas slowly$`, scriptedSlow)
	sc.Step(`^flow "([^"]*)" declares on_detach cancel_after "([^"]*)"$`, detachPolicy)

	sc.Step(`^user "([^"]*)" starts a run of "([^"]*)" and receives (\d+) deltas?$`, startRunReceiving)
	sc.Step(`^user "([^"]*)" starts a run of "([^"]*)"$`, startRun)
	sc.Step(`^user "([^"]*)" attaches to ([A-Za-z]+)'s run$`, userAttaches)
	sc.Step(`^([A-Za-z]+) detaches$`, userDetaches)
	sc.Step(`^([A-Za-z]+) reattaches with (?:her|his|their) last cursor$`, userReattaches)
	sc.Step(`^the scripted model streams (\d+) more deltas$`, scriptedStreamsMore)
	sc.Step(`^the scripted model finishes$`, scriptedFinishes)
	sc.Step(`^the run completes$`, runCompletes)
	sc.Step(`^([A-Za-z]+) cancels the run$`, userCancels)
	sc.Step(`^(\d+) minutes? pass(?:es)? with no client attached$`, minutesPassUnattended)

	sc.Step(`^([A-Za-z]+) receives each durable event after her cursor exactly once and in order$`, receivesDurableOnce)
	sc.Step(`^([A-Za-z]+)'s stream contains message text "([^"]*)" exactly once$`, streamHasTextOnce)
	sc.Step(`^([A-Za-z]+) receives a snapshot covering the first (\d+) deltas$`, receivesSnapshot)
	sc.Step(`^([A-Za-z]+) then receives deltas (\d+) to (\d+) with no gap and no duplicate$`, receivesDeltasAfter)
	sc.Step(`^([A-Za-z]+) and ([A-Za-z]+) received the same durable events in the same order$`, sameDurableOrder)
	sc.Step(`^([A-Za-z]+) receives a reset for the message before the deltas of attempt (\d+)$`, resetBeforeAttempt)
	sc.Step(`^the completed message text is "([^"]*)"$`, completedText)
	sc.Step(`^the model step stops within one heartbeat interval$`, modelStops)
	sc.Step(`^the run state becomes "([^"]*)"$`, runStateBecomes)
	sc.Step(`^the message is recorded as interrupted with the text received so far$`, messageInterrupted)
}

func streamWorldOrError(ctx context.Context) (*streamWorld, error) {
	w := world(ctx).Stream
	if w == nil || w.live == nil {
		return nil, errStreamNoWorld
	}
	return w, nil
}

func scriptedModel(ctx context.Context, _ string, text string, deltas int) error {
	w := world(ctx).Stream
	if w == nil {
		return errStreamNoWorld
	}
	return w.script(text, deltas)
}

func scriptedFailure(ctx context.Context, _ string, failAfter int, text string, deltas int) error {
	w := world(ctx).Stream
	if w == nil {
		return errStreamNoWorld
	}
	return w.failingScript(failAfter, text, deltas)
}

func scriptedSlow(ctx context.Context, _ string, deltas int) error {
	w := world(ctx).Stream
	if w == nil {
		return errStreamNoWorld
	}
	return w.script(strings.Repeat("x", deltas), deltas)
}

func detachPolicy(ctx context.Context, _ string, duration string) error {
	w := world(ctx).Stream
	if w == nil {
		return errStreamNoWorld
	}
	d, err := time.ParseDuration(duration)
	if err != nil {
		return fmt.Errorf("on_detach duration %q: %w", duration, err)
	}
	w.detachAfter = d
	return nil
}

func startRunReceiving(ctx context.Context, user, _ string, deltas int) error {
	w := world(ctx).Stream
	if w == nil {
		return errStreamNoWorld
	}
	return w.start(ctx, user, deltas)
}

func startRun(ctx context.Context, user, _ string) error {
	w := world(ctx).Stream
	if w == nil {
		return errStreamNoWorld
	}
	n := 0
	if w.failing {
		n = w.failAfter
	}
	return w.start(ctx, user, n)
}

func userAttaches(ctx context.Context, user, owner string) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	if _, ok := w.clients[owner]; !ok {
		return fmt.Errorf("no client %q", owner)
	}
	if err := w.attach(user); err != nil {
		return err
	}
	w.drain()
	return nil
}

func userDetaches(ctx context.Context, user string) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.detach(user)
}

func userReattaches(ctx context.Context, user string) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	if err := w.reattach(user); err != nil {
		return err
	}
	w.drain()
	return nil
}

func scriptedStreamsMore(ctx context.Context, n int) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.stream(w.tenantCtx(), n)
}

func scriptedFinishes(ctx context.Context) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.finish(ctx)
}

func runCompletes(ctx context.Context) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.finish(ctx)
}

func userCancels(ctx context.Context, user string) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.cancel(user)
}

func minutesPassUnattended(ctx context.Context, minutes int) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.advance(time.Duration(minutes) * time.Minute)
}

func receivesDurableOnce(ctx context.Context, user string) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	c, err := w.client(user)
	if err != nil {
		return err
	}
	want := c.detachCursor.Seq + 1
	seen := map[int64]bool{}
	for _, f := range c.frames[c.mark:] {
		if f.Durable == nil {
			continue
		}
		seq := int64(f.Durable.Seq)
		if seen[seq] {
			return fmt.Errorf("%s received durable event %d twice", user, seq)
		}
		seen[seq] = true
		if seq != want {
			return fmt.Errorf("%s received durable event %d, want %d in order", user, seq, want)
		}
		want++
	}
	if len(seen) == 0 {
		return fmt.Errorf("%s received no durable event after her cursor", user)
	}
	return nil
}

func streamHasTextOnce(ctx context.Context, user, text string) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	c, err := w.client(user)
	if err != nil {
		return err
	}
	var all strings.Builder
	for _, f := range c.frames {
		switch {
		case f.Durable != nil && f.Durable.Type == "message.completed":
			var data struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(f.Durable.Data, &data); err == nil {
				all.WriteString(data.Text)
			}
		case f.Snapshot != nil:
			all.WriteString(f.Snapshot.Text)
		case f.Delta != nil && f.Delta.Kind == "text":
			all.WriteString(f.Delta.Text)
		}
	}
	if got := strings.Count(all.String(), text); got != 1 {
		return fmt.Errorf("%s's stream contains %q %d times, want exactly once", user, text, got)
	}
	return nil
}

func receivesSnapshot(ctx context.Context, user string, first int) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	c, err := w.client(user)
	if err != nil {
		return err
	}
	if first > len(w.chunks) {
		return fmt.Errorf("the scripted model has only %d deltas", len(w.chunks))
	}
	want := streamTextOf(w.chunks[:first]...)
	for _, f := range c.frames[c.mark:] {
		if f.Snapshot == nil {
			continue
		}
		if f.Snapshot.DSeq != int64(first) {
			return fmt.Errorf("snapshot covers dseq %d, want %d", f.Snapshot.DSeq, first)
		}
		if f.Snapshot.Text != want {
			return fmt.Errorf("snapshot text %q, want %q", f.Snapshot.Text, want)
		}
		return nil
	}
	return fmt.Errorf("%s received no snapshot", user)
}

func receivesDeltasAfter(ctx context.Context, user string, from, to int) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	c, err := w.client(user)
	if err != nil {
		return err
	}
	var got []stream.Delta
	for _, f := range c.frames[c.mark:] {
		if f.Delta != nil && f.Delta.Kind == "text" {
			got = append(got, *f.Delta)
		}
	}
	if len(got) != to-from+1 {
		return fmt.Errorf("%s received %d deltas after the snapshot, want %d", user, len(got), to-from+1)
	}
	for i, delta := range got {
		want := int64(from + i)
		if delta.DSeq != want {
			return fmt.Errorf("%s received dseq %d, want %d", user, delta.DSeq, want)
		}
		text := w.chunks[from-1+i]
		if delta.Text != text {
			return fmt.Errorf("%s received text %q at dseq %d, want %q", user, delta.Text, delta.DSeq, text)
		}
	}
	return nil
}

func sameDurableOrder(ctx context.Context, first, second string) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	a := w.durable(first)
	b := w.durable(second)
	if len(a) != len(b) {
		return fmt.Errorf("%s received %d durable events, %s %d", first, len(a), second, len(b))
	}
	for i := range a {
		if a[i].Seq != b[i].Seq || a[i].Type != b[i].Type {
			return fmt.Errorf("durable event %d: %s got %d %s, %s got %d %s",
				i, first, a[i].Seq, a[i].Type, second, b[i].Seq, b[i].Type)
		}
	}
	return nil
}

func resetBeforeAttempt(ctx context.Context, user string, attempt int) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	c, err := w.client(user)
	if err != nil {
		return err
	}
	resetAt, firstAt := -1, -1
	for i, f := range c.frames {
		if f.Delta == nil {
			continue
		}
		if f.Delta.Kind == "reset" && f.Delta.Attempt == attempt && resetAt < 0 {
			resetAt = i
		}
		if f.Delta.Kind == "text" && f.Delta.Attempt == attempt && firstAt < 0 {
			firstAt = i
		}
	}
	if resetAt < 0 {
		return fmt.Errorf("%s received no reset for attempt %d", user, attempt)
	}
	if firstAt < 0 {
		return fmt.Errorf("%s received no delta of attempt %d", user, attempt)
	}
	if resetAt > firstAt {
		return fmt.Errorf("the attempt %d delta came before the reset", attempt)
	}
	return nil
}

func completedText(ctx context.Context, text string) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	data, err := w.completedMessage("ana")
	if err != nil {
		return err
	}
	if got, _ := data["text"].(string); got != text {
		return fmt.Errorf("completed message text %q, want %q", got, text)
	}
	if interrupted, _ := data["interrupted"].(bool); interrupted {
		return fmt.Errorf("completed message is marked interrupted")
	}
	return nil
}

func modelStops(ctx context.Context) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	tenant := w.tenantCtx()
	if err := w.live.Heartbeat(tenant, w.runID); core.CodeOf(err) != core.CodeCancelled {
		return fmt.Errorf("heartbeat after the cancellation: %w, want cancelled", err)
	}
	sent := w.sent
	if _, err := w.live.Publish(tenant, w.runID, stream.Delta{MessageID: w.messageID, Kind: "text", Text: "late"}); core.CodeOf(err) != core.CodeCancelled {
		return fmt.Errorf("publish after the cancellation: %w, want cancelled", err)
	}
	w.clock.Advance(w.live.HeartbeatInterval())
	if err := w.live.Heartbeat(tenant, w.runID); core.CodeOf(err) != core.CodeCancelled {
		return fmt.Errorf("heartbeat one interval later: %w, want cancelled", err)
	}
	if w.sent != sent {
		return fmt.Errorf("the model kept streaming after the cancellation")
	}
	return nil
}

func runStateBecomes(ctx context.Context, state string) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	got, err := w.state(ctx)
	if err != nil {
		return err
	}
	if got != state {
		return fmt.Errorf("run state %q, want %q", got, state)
	}
	return nil
}

func messageInterrupted(ctx context.Context) error {
	w, err := streamWorldOrError(ctx)
	if err != nil {
		return err
	}
	data, err := w.completedMessage("ana")
	if err != nil {
		return err
	}
	if interrupted, _ := data["interrupted"].(bool); !interrupted {
		return fmt.Errorf("the message is not marked interrupted")
	}
	want := streamTextOf(w.chunks[:w.sent]...)
	if got, _ := data["text"].(string); got != want {
		return fmt.Errorf("partial text %q, want %q", got, want)
	}
	return nil
}
