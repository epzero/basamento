package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/stream/eventlog"
)

var (
	quotedRe       = regexp.MustCompile(`"([^"]*)"`)
	repeatedListRe = regexp.MustCompile(`^"([^"]*)" repeated (\d+) times$`)
)

func registerEventlog(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, scn *godog.Scenario) (context.Context, error) {
		if !scenarioUsesEventlog(scn) {
			return ctx, nil
		}
		w := world(ctx)
		w.Eventlog = newEventlogWorld()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Eventlog; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	sc.Step(`^a tenant "([^"]*)" with a durable event log$`, durableEventLog)
	sc.Step(`^a run "([^"]*)" of tenant "([^"]*)" in state "([^"]*)"$`, givenRun)
	sc.Step(`^"([^"]*)" is in state "([^"]*)" and the bus is unreachable$`, runRunningWithBusDown)
	sc.Step(`^the log of "([^"]*)" holds events with sequences (\d+) to (\d+) and types (.*)$`, logHoldsEvents)
	sc.Step(`^the state of "([^"]*)" changes to "([^"]*)" and records events (.*)$`, stateChangesRecording)
	sc.Step(`^the state of "([^"]*)" changes to "([^"]*)" and records an event whose write is rejected$`, stateChangeRejected)
	sc.Step(`^the process stops at "([^"]*)" during a state change to "([^"]*)" that records (\d+) events$`, processStopsDuringChange)
	sc.Step(`^(\d+) state changes of "([^"]*)" and (\d+) state changes of "([^"]*)" are recorded at the same time, each recording "([^"]*)"$`, concurrentStateChanges)
	sc.Step(`^(\d+) state changes of "([^"]*)" and (\d+) state changes of "([^"]*)" are recorded at the same time, each recording "([^"]*)", while the principal of "([^"]*)" reads "([^"]*)" after sequence (\d+) and then reads again after they finish from the resulting cursor$`, concurrentChangesDuringRead)

	sc.Step(`^the relay runs and the bus accepts$`, relayRunsAndBusAccepts)
	sc.Step(`^the relay runs$`, relayRuns)
	sc.Step(`^the relay has run$`, relayRuns)
	sc.Step(`^the relay stops at "([^"]*)"$`, relayStops)
	sc.Step(`^the relay restarts and the bus accepts$`, relayRestarts)
	sc.Step(`^the bus is unreachable after accepting (\d+) publish(?:es)?$`, busUnreachableAfter)
	sc.Step(`^the bus becomes reachable$`, busBecomesReachable)

	sc.Step(`^the principal of "([^"]*)" reads "([^"]*)" after sequence (\d+)$`, principalReads)
	sc.Step(`^a caller without a tenant reads "([^"]*)" after sequence (\d+)$`, callerWithoutTenantReads)

	sc.Step(`^the log of "([^"]*)" holds either no events and state "([^"]*)" or events with sequences (\d+) to (\d+) and state "([^"]*)"$`, logHoldsWholeOrNothing)
	sc.Step(`^the state of "([^"]*)" in the log is "([^"]*)"$`, logStateIs)
	sc.Step(`^the log of "([^"]*)" holds no events$`, logHoldsNoEvents)
	sc.Step(`^the write fails with code "([^"]*)"$`, writeFailsWithCode)
	sc.Step(`^bus "([^"]*)" received from "([^"]*)" sequences \[([^\]]*)\] in this order$`, busReceivedSequences)
	sc.Step(`^bus "([^"]*)" received no event from "([^"]*)"$`, busReceivedNothing)
	sc.Step(`^outbox entries (\d+) to (\d+) of "([^"]*)" are marked relayed$`, outboxEntriesRelayed)
	sc.Step(`^outbox entries (\d+) to (\d+) of "([^"]*)" are marked relayed and entry (\d+) is not$`, outboxEntriesRelayedExcept)
	sc.Step(`^a second relay run publishes nothing to "([^"]*)"$`, secondRelayPublishesNothing)
	sc.Step(`^bus "([^"]*)" delivered (\d+) copies of the event "([^"]*)" with sequence (\d+)$`, busDeliveredCopies)
	sc.Step(`^the (\d+) copies carry the same run id, sequence, event type, schema version and data$`, copiesAreIdentical)
	sc.Step(`^the read returns events with sequences (\d+) to (\d+) and types (.*)$`, readReturnsEvents)
	sc.Step(`^the read returns no events$`, readReturnsNothing)
	sc.Step(`^the two reads together return events of "([^"]*)" with sequences (\d+) to (\d+) in order with no gap and no duplicate$`, twoReadsAreContiguous)
	sc.Step(`^the event of "([^"]*)" with sequence (\d+) on the bus has fields \[(.*)\]$`, envelopeHasFields)
}

func eventlogWorldOrError(ctx context.Context) (*eventlogWorld, error) {
	w := world(ctx).Eventlog
	if w == nil || w.log == nil {
		return nil, errEventlogNoWorld
	}
	return w, nil
}

func durableEventLog(ctx context.Context, tenant string) error {
	w := world(ctx).Eventlog
	if w == nil {
		return errEventlogNoWorld
	}
	if w.store == nil {
		if err := w.open(ctx); err != nil {
			return err
		}
		w.tenant = tenant
	}
	return nil
}

func givenRun(ctx context.Context, runID, tenant, state string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.ensureRun(ctx, tenant, runID, state)
}

func runRunningWithBusDown(ctx context.Context, runID, state string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	if err := w.ensureRun(ctx, w.tenant, runID, state); err != nil {
		return err
	}
	w.bus.setDown(true)
	return nil
}

func logHoldsEvents(ctx context.Context, runID string, from, to int, typesSpec string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	types, err := parseTypeList(typesSpec)
	if err != nil {
		return err
	}
	events, err := w.log.Read(w.tenantCtx(w.tenant), runID, 0)
	if err != nil && core.CodeOf(err) != core.CodeNotFound {
		return err
	}
	if len(events) == 0 {
		if err := w.ensureRun(ctx, w.tenant, runID, "running"); err != nil {
			return err
		}
		if err := w.appendTypes(ctx, w.tenant, runID, "running", types); err != nil {
			return err
		}
	}
	return w.assertLog(ctx, runID, uint64(from), uint64(to), types)
}

func (w *eventlogWorld) assertLog(ctx context.Context, runID string, from, to uint64, types []string) error {
	events, err := w.log.Read(w.tenantCtx(w.tenant), runID, 0)
	if err != nil {
		return err
	}
	if uint64(len(events)) != to-from+1 {
		return fmt.Errorf("%s holds %d events, want sequences %d to %d", runID, len(events), from, to)
	}
	if len(types) != len(events) {
		return fmt.Errorf("%s holds %d events but the scenario names %d types", runID, len(events), len(types))
	}
	for i, e := range events {
		if e.Seq != from+uint64(i) {
			return fmt.Errorf("%s event %d has sequence %d, want %d", runID, i, e.Seq, from+uint64(i))
		}
		if e.Type != types[i] {
			return fmt.Errorf("%s event %d has type %q, want %q", runID, i, e.Type, types[i])
		}
	}
	return nil
}

func stateChangesRecording(ctx context.Context, runID, state, typesSpec string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	types, err := parseTypeList(typesSpec)
	if err != nil {
		return err
	}
	w.lastErr = w.appendTypes(ctx, w.tenant, runID, state, types)
	return nil
}

func stateChangeRejected(ctx context.Context, runID, state string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	w.crashAt = "the first event written"
	w.crashCode = core.CodeConflict
	w.lastErr = w.appendTypes(ctx, w.tenant, runID, state, []string{"node.completed"})
	w.crashAt = ""
	w.crashCode = ""
	return nil
}

func processStopsDuringChange(ctx context.Context, point, state string, count int) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	w.crashAt = point
	types := []string{"node.completed", "run.completed"}
	if count != len(types) {
		types = types[:count]
	}
	w.lastErr = w.appendTypes(ctx, w.tenant, "r1", state, types)
	w.crashAt = ""
	return nil
}

func concurrentStateChanges(ctx context.Context, n1 int, run1 string, n2 int, run2 string, typ string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.runConcurrent(ctx, n1, run1, n2, run2, typ, nil)
}

func concurrentChangesDuringRead(ctx context.Context, n1 int, run1 string, n2 int, run2 string, typ, tenant, readRun string, after int) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	var first []eventlog.Event
	var firstErr error
	during := func() {
		first, firstErr = w.log.Read(w.tenantCtx(tenant), readRun, uint64(after))
	}
	if err := w.runConcurrent(ctx, n1, run1, n2, run2, typ, during); err != nil {
		return err
	}
	if firstErr != nil {
		w.lastReadErr = firstErr
		return nil
	}
	cursor := uint64(after)
	if len(first) > 0 {
		cursor = first[len(first)-1].Seq
	}
	second, err := w.log.Read(w.tenantCtx(tenant), readRun, cursor)
	if err != nil {
		w.lastReadErr = err
		return nil
	}
	w.lastRead = append(append([]eventlog.Event{}, first...), second...)
	w.lastReadErr = nil
	return nil
}

func (w *eventlogWorld) runConcurrent(ctx context.Context, n1 int, run1 string, n2 int, run2 string, typ string, during func()) error {
	type call struct {
		runID string
	}
	var calls []call
	for i := 0; i < n1; i++ {
		calls = append(calls, call{run1})
	}
	for i := 0; i < n2; i++ {
		calls = append(calls, call{run2})
	}
	start := make(chan struct{})
	errs := make(chan error, len(calls))
	var wg sync.WaitGroup
	for _, c := range calls {
		wg.Add(1)
		go func(runID string) {
			defer wg.Done()
			<-start
			_, err := w.log.AppendChange(w.tenantCtx(w.tenant), runID, "running", []eventlog.NewEvent{{Type: typ}})
			errs <- err
		}(c.runID)
	}
	close(start)
	if during != nil {
		during()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func relayRunsAndBusAccepts(ctx context.Context) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	w.bus.setReachable()
	return w.relay.Run(ctx)
}

func relayRuns(ctx context.Context) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.relay.Run(ctx)
}

func relayStops(ctx context.Context, point string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	w.relayStopAt = point
	err = w.relay.Run(ctx)
	w.relayStopAt = ""
	return err
}

func relayRestarts(ctx context.Context) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	w.relayStopAt = ""
	w.bus.setReachable()
	return w.relay.Run(ctx)
}

func busUnreachableAfter(ctx context.Context, n int) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	w.bus.setUnreachableAfter(n)
	return nil
}

func busBecomesReachable(ctx context.Context) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	w.bus.setReachable()
	return nil
}

func principalReads(ctx context.Context, tenant, runID string, after int) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	w.lastRead, w.lastReadErr = w.log.Read(w.tenantCtx(tenant), runID, uint64(after))
	return nil
}

func callerWithoutTenantReads(ctx context.Context, runID string, after int) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	w.lastRead, w.lastReadErr = w.log.Read(context.Background(), runID, uint64(after))
	return nil
}

func logHoldsWholeOrNothing(ctx context.Context, runID, okState string, from, to int, finalState string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	events, err := w.log.Read(w.tenantCtx(w.tenant), runID, 0)
	if err != nil {
		return err
	}
	state, err := w.log.State(w.tenantCtx(w.tenant), runID)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		if state != okState {
			return fmt.Errorf("%s holds no events but is %q, want %q", runID, state, okState)
		}
		return nil
	}
	if uint64(len(events)) != uint64(to-from+1) {
		return fmt.Errorf("%s holds %d events, want none or sequences %d to %d", runID, len(events), from, to)
	}
	for i, e := range events {
		if e.Seq != uint64(from+i) {
			return fmt.Errorf("%s event %d has sequence %d, want %d", runID, i, e.Seq, from+i)
		}
	}
	if state != finalState {
		return fmt.Errorf("%s holds all events but is %q, want %q", runID, state, finalState)
	}
	return nil
}

func logStateIs(ctx context.Context, runID, want string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	got, err := w.log.State(w.tenantCtx(w.tenant), runID)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("state of %s is %q, want %q", runID, got, want)
	}
	return nil
}

func logHoldsNoEvents(ctx context.Context, runID string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	events, err := w.log.Read(w.tenantCtx(w.tenant), runID, 0)
	if err != nil {
		return err
	}
	if len(events) != 0 {
		return fmt.Errorf("%s holds %d events, want none", runID, len(events))
	}
	return nil
}

func writeFailsWithCode(ctx context.Context, code string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	if w.lastErr == nil {
		return fmt.Errorf("write succeeded, want %s", code)
	}
	if got := string(core.CodeOf(w.lastErr)); got != code {
		return fmt.Errorf("write failed with %s, want %s", got, code)
	}
	return nil
}

func busReceivedSequences(ctx context.Context, tenant, runID, list string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	want, err := parseIntList(list)
	if err != nil {
		return err
	}
	envs := w.bus.forRun(runID)
	if len(envs) != len(want) {
		return fmt.Errorf("bus %s received %d events from %s, want %v", tenant, len(envs), runID, want)
	}
	wantSubject, err := eventlog.RunSubject(tenant, runID)
	if err != nil {
		return err
	}
	for i, e := range envs {
		if e.Seq != want[i] {
			return fmt.Errorf("bus %s event %d from %s has sequence %d, want %d", tenant, i, runID, e.Seq, want[i])
		}
		if subject := w.bus.subjectsFor(runID)[i]; subject != wantSubject {
			return fmt.Errorf("bus %s event %d has subject %q", tenant, i, subject)
		}
	}
	return nil
}

func busReceivedNothing(ctx context.Context, tenant, runID string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	if envs := w.bus.forRun(runID); len(envs) != 0 {
		return fmt.Errorf("bus %s received %d events from %s, want none", tenant, len(envs), runID)
	}
	return nil
}

func outboxEntriesRelayed(ctx context.Context, from, to int, runID string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.assertOutbox(runID, from, to, 0)
}

func outboxEntriesRelayedExcept(ctx context.Context, from, to int, runID string, except int) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	return w.assertOutbox(runID, from, to, except)
}

func (w *eventlogWorld) assertOutbox(runID string, from, to, except int) error {
	entries, err := w.log.Outbox(w.tenantCtx(w.tenant), runID)
	if err != nil {
		return err
	}
	bySeq := map[uint64]eventlog.OutboxEntry{}
	for _, e := range entries {
		bySeq[e.Seq] = e
	}
	for seq := from; seq <= to; seq++ {
		e, ok := bySeq[uint64(seq)]
		if !ok {
			return fmt.Errorf("outbox of %s has no entry %d", runID, seq)
		}
		if seq == except {
			if e.Relayed {
				return fmt.Errorf("outbox entry %d of %s is relayed, want not", seq, runID)
			}
			continue
		}
		if !e.Relayed {
			return fmt.Errorf("outbox entry %d of %s is not relayed", seq, runID)
		}
	}
	return nil
}

func secondRelayPublishesNothing(ctx context.Context, tenant string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	before := w.bus.count()
	if err := w.relay.Run(ctx); err != nil {
		return err
	}
	if after := w.bus.count(); after != before {
		return fmt.Errorf("second relay run published %d events to %s, want none", after-before, tenant)
	}
	return nil
}

func (b *eventBus) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.envs)
}

func busDeliveredCopies(ctx context.Context, tenant string, copies int, runID string, seq int) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	got := w.bus.copies(runID, uint64(seq))
	if len(got) != copies {
		return fmt.Errorf("bus %s delivered %d copies of %s sequence %d, want %d", tenant, len(got), runID, seq, copies)
	}
	return nil
}

func (b *eventBus) copies(runID string, seq uint64) []eventlog.Envelope {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []eventlog.Envelope
	for _, m := range b.envs {
		if m.env.RunID == runID && m.env.Seq == seq {
			out = append(out, m.env)
		}
	}
	return out
}

func copiesAreIdentical(ctx context.Context, copies int) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	var all []eventlog.Envelope
	seen := map[string]bool{}
	for _, runID := range w.bus.runIDs() {
		for _, seq := range w.bus.sequences(runID) {
			envs := w.bus.copies(runID, seq)
			key := fmt.Sprintf("%s/%d", runID, seq)
			if len(envs) > 1 && !seen[key] {
				seen[key] = true
				all = append(all, envs...)
			}
		}
	}
	if len(all) != copies {
		return fmt.Errorf("found %d copies of repeated events, want %d", len(all), copies)
	}
	first := all[0]
	for _, e := range all[1:] {
		if e.RunID != first.RunID || e.Seq != first.Seq || e.Type != first.Type || e.V != first.V || string(e.Data) != string(first.Data) {
			return fmt.Errorf("copies differ: %+v and %+v", first, e)
		}
	}
	return nil
}

func (b *eventBus) runIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, m := range b.envs {
		if !seen[m.env.RunID] {
			seen[m.env.RunID] = true
			out = append(out, m.env.RunID)
		}
	}
	return out
}

func (b *eventBus) sequences(runID string) []uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	seen := map[uint64]bool{}
	var out []uint64
	for _, m := range b.envs {
		if m.env.RunID == runID && !seen[m.env.Seq] {
			seen[m.env.Seq] = true
			out = append(out, m.env.Seq)
		}
	}
	return out
}

func readReturnsEvents(ctx context.Context, from, to int, typesSpec string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	if w.lastReadErr != nil {
		return w.lastReadErr
	}
	types, err := parseTypeList(typesSpec)
	if err != nil {
		return err
	}
	if len(w.lastRead) != to-from+1 {
		return fmt.Errorf("read returned %d events, want sequences %d to %d", len(w.lastRead), from, to)
	}
	for i, e := range w.lastRead {
		if e.Seq != uint64(from+i) {
			return fmt.Errorf("read event %d has sequence %d, want %d", i, e.Seq, from+i)
		}
		if e.Type != types[i] {
			return fmt.Errorf("read event %d has type %q, want %q", i, e.Type, types[i])
		}
	}
	return nil
}

func readReturnsNothing(ctx context.Context) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	if w.lastReadErr != nil {
		return w.lastReadErr
	}
	if len(w.lastRead) != 0 {
		return fmt.Errorf("read returned %d events, want none", len(w.lastRead))
	}
	return nil
}

func readFailsWithCode(ctx context.Context, code string) error {
	if c := world(ctx).Conversation; c != nil && c.lastErr != nil {
		return checkConvErr(c, code)
	}
	if w := world(ctx).Eventlog; w != nil && w.log != nil {
		if w.lastReadErr == nil {
			return fmt.Errorf("read succeeded, want %s", code)
		}
		if got := string(core.CodeOf(w.lastReadErr)); got != code {
			return fmt.Errorf("read failed with %s, want %s", got, code)
		}
		return nil
	}
	var err error
	if b := world(ctx).Bus; b != nil {
		err = b.lastReadErr
	}
	if err == nil {
		return fmt.Errorf("read succeeded, want %s", code)
	}
	if got := string(core.CodeOf(err)); got != code {
		return fmt.Errorf("read failed with %s, want %s", got, code)
	}
	return nil
}

func twoReadsAreContiguous(ctx context.Context, runID string, from, to int) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	if w.lastReadErr != nil {
		return w.lastReadErr
	}
	if len(w.lastRead) != to-from+1 {
		return fmt.Errorf("the two reads returned %d events of %s, want %d", len(w.lastRead), runID, to-from+1)
	}
	for i, e := range w.lastRead {
		if e.Seq != uint64(from+i) {
			return fmt.Errorf("event %d has sequence %d, want %d (gap or duplicate)", i, e.Seq, from+i)
		}
	}
	return nil
}

func envelopeHasFields(ctx context.Context, runID string, seq int, fields string) error {
	w, err := eventlogWorldOrError(ctx)
	if err != nil {
		return err
	}
	envs := w.bus.copies(runID, uint64(seq))
	if len(envs) != 1 {
		return fmt.Errorf("bus holds %d envelopes for %s sequence %d, want 1", len(envs), runID, seq)
	}
	env := envs[0]
	for _, field := range strings.Split(fields, ", ") {
		field = strings.TrimSpace(field)
		switch {
		case strings.HasPrefix(field, "run_id "):
			if want, err := unquote(strings.TrimPrefix(field, "run_id ")); err != nil || env.RunID != want {
				return fmt.Errorf("envelope run_id %q, want %s", env.RunID, field)
			}
		case strings.HasPrefix(field, "seq "):
			want, err := strconv.ParseUint(strings.TrimPrefix(field, "seq "), 10, 64)
			if err != nil || env.Seq != want {
				return fmt.Errorf("envelope seq %d, want %s", env.Seq, field)
			}
		case strings.HasPrefix(field, "type "):
			if want, err := unquote(strings.TrimPrefix(field, "type ")); err != nil || env.Type != want {
				return fmt.Errorf("envelope type %q, want %s", env.Type, field)
			}
		case strings.HasPrefix(field, "v "):
			want, err := strconv.Atoi(strings.TrimPrefix(field, "v "))
			if err != nil || env.V != want {
				return fmt.Errorf("envelope v %d, want %s", env.V, field)
			}
		case field == "data present":
			if len(env.Data) == 0 || !json.Valid(env.Data) {
				return fmt.Errorf("envelope data %q, want present JSON", env.Data)
			}
		default:
			return fmt.Errorf("unknown envelope field %q", field)
		}
	}
	return nil
}

func unquote(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("not a quoted literal: %s", s)
	}
	return s[1 : len(s)-1], nil
}

func parseTypeList(spec string) ([]string, error) {
	spec = strings.TrimSpace(spec)
	spec = strings.TrimPrefix(spec, "[")
	spec = strings.TrimSuffix(spec, "]")
	spec = strings.TrimSpace(spec)
	if m := repeatedListRe.FindStringSubmatch(spec); m != nil {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return nil, err
		}
		out := make([]string, n)
		for i := range out {
			out[i] = m[1]
		}
		return out, nil
	}
	matches := quotedRe.FindAllStringSubmatch(spec, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no event types in %q", spec)
	}
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m[1]
	}
	return out, nil
}

func parseIntList(spec string) ([]uint64, error) {
	parts := strings.Split(spec, ",")
	out := make([]uint64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}
