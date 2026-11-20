package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"
	"go.opentelemetry.io/otel/propagation"

	"github.com/epzero/basamento/core"
)

func registerTelemetrySpans(sc *godog.ScenarioContext) {
	sc.Step(`^a? ?span "([^"]*)" is started and ended$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		w.startSpan(w.currentCtx(), name)
		ref, _ := w.span(name)
		ref.span.End()
		return nil
	})
	sc.Step(`^span "([^"]*)" is started$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		w.startSpan(w.currentCtx(), name)
		return nil
	})
	sc.Step(`^a span "([^"]*)" is started$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		w.startSpan(w.currentCtx(), name)
		return nil
	})
	sc.Step(`^span "([^"]*)" is started with parent span "([^"]*)"$`, func(ctx context.Context, name, parent string) error {
		w := world(ctx).Telemetry
		ref, err := w.span(parent)
		if err != nil {
			return err
		}
		w.startSpan(ref.ctx, name)
		return nil
	})
	sc.Step(`^span "([^"]*)" is ended$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		ref, err := w.span(name)
		if err != nil {
			return err
		}
		ref.span.End()
		return nil
	})
	sc.Step(`^span "([^"]*)" is ended with status "([^"]*)"$`, func(ctx context.Context, name, status string) error {
		w := world(ctx).Telemetry
		ref, err := w.span(name)
		if err != nil {
			return err
		}
		ref.span.SetStatus(statusCode(status), status)
		ref.span.End()
		return nil
	})
	sc.Step(`^span "([^"]*)" is started and ended with status "([^"]*)"$`, func(ctx context.Context, name, status string) error {
		w := world(ctx).Telemetry
		w.startSpan(w.currentCtx(), name)
		ref, _ := w.span(name)
		ref.span.SetStatus(statusCode(status), status)
		ref.span.End()
		return nil
	})
	sc.Step(`^span "([^"]*)" is started and ended inside run "([^"]*)"$`, func(ctx context.Context, name, run string) error {
		w := world(ctx).Telemetry
		w.startSpan(w.ctxFor(w.tenant, run), name)
		ref, _ := w.span(name)
		ref.span.End()
		return nil
	})
	sc.Step(`^spans "([^"]*)" and "([^"]*)" are started and ended$`, func(ctx context.Context, first, second string) error {
		w := world(ctx).Telemetry
		for _, name := range []string{first, second} {
			w.startSpan(w.currentCtx(), name)
			ref, _ := w.span(name)
			ref.span.End()
		}
		return nil
	})
	sc.Step(`^span "([^"]*)" is started and ended, then the clock advances by (\d+) seconds?, then span "([^"]*)" is started and ended, then the clock advances by (\d+) seconds?, then span "([^"]*)" is started and ended$`, func(ctx context.Context, first string, firstSeconds int, second string, secondSeconds int, third string) error {
		w := world(ctx).Telemetry
		steps := []struct {
			name    string
			advance int
		}{{first, firstSeconds}, {second, secondSeconds}, {third, 0}}
		for _, item := range steps {
			w.startSpan(w.currentCtx(), item.name)
			ref, _ := w.span(item.name)
			ref.span.End()
			if item.advance > 0 {
				world(ctx).Core.clock.Advance(time.Duration(item.advance) * time.Second)
			}
		}
		return nil
	})
	sc.Step(`^span "([^"]*)" records attribute "([^"]*)" with the value (.*)$`, func(ctx context.Context, name, key, value string) error {
		ref, err := world(ctx).Telemetry.span(name)
		if err != nil {
			return err
		}
		setAttribute(ref.span, key, parseValue(value))
		return nil
	})
	sc.Step(`^the trace context of span "([^"]*)" is written to a carrier and read back$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		ref, err := w.span(name)
		if err != nil {
			return err
		}
		carrier := propagation.MapCarrier{}
		w.setup.Inject(ref.ctx, carrier)
		w.restored = w.setup.Extract(context.Background(), carrier)
		return nil
	})
	sc.Step(`^span "([^"]*)" is started from the restored trace context and ended$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		if w.restored == nil {
			return fmt.Errorf("no restored trace context: the carrier step must run first")
		}
		w.startSpan(w.restored, name)
		ref, _ := w.span(name)
		ref.span.End()
		return nil
	})

	sc.Step(`^span "([^"]*)" starts at "([^"]*)" and ends at "([^"]*)"$`, func(ctx context.Context, name, start, end string) error {
		span, ok := world(ctx).Telemetry.setup.SpanByName(name)
		if !ok {
			return fmt.Errorf("span %q not kept", name)
		}
		return compareTimes(span.Start, span.End, start, end)
	})
	sc.Step(`^the log record "([^"]*)" and the metric point "([^"]*)" have time "([^"]*)"$`, func(ctx context.Context, message, name, at string) error {
		want, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return err
		}
		var logTime, metricTime time.Time
		for _, rec := range world(ctx).Telemetry.setup.Logs() {
			if rec.Message == message {
				logTime = rec.Time
			}
		}
		for _, point := range world(ctx).Telemetry.setup.Metrics() {
			if point.Name == name {
				metricTime = point.Time
			}
		}
		if !logTime.Equal(want) || !metricTime.Equal(want) {
			return fmt.Errorf("log time %v metric time %v, want %v", logTime, metricTime, want)
		}
		return nil
	})
	sc.Step(`^span "([^"]*)" lasts (\d+) milliseconds?$`, func(ctx context.Context, name string, ms int) error {
		span, ok := world(ctx).Telemetry.setup.SpanByName(name)
		if !ok {
			return fmt.Errorf("span %q not kept", name)
		}
		if got := span.Duration(); got != time.Duration(ms)*time.Millisecond {
			return fmt.Errorf("span %q lasts %v, want %dms", name, got, ms)
		}
		return nil
	})
	sc.Step(`^the span store holds (\d+) spans? named "([^"]*)"$`, func(ctx context.Context, count int, name string) error {
		spans, err := world(ctx).Telemetry.readSpans(world(ctx).Telemetry.tenant, world(ctx).Telemetry.run)
		if err != nil {
			return err
		}
		n := 0
		for _, span := range spans {
			if span.Name == name {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("span store holds %d spans named %q, want %d", n, name, count)
		}
		return nil
	})
	sc.Step(`^the span store holds (\d+) spans$`, func(ctx context.Context, count int) error {
		var n int
		w := world(ctx).Telemetry
		if w.store == nil {
			return fmt.Errorf("no span store in this profile")
		}
		if err := w.store.Execer().QueryRow(ctx, `SELECT COUNT(*) FROM telemetry_spans`).Scan(&n); err != nil {
			return err
		}
		if n != count {
			return fmt.Errorf("span store holds %d spans, want %d", n, count)
		}
		return nil
	})
	sc.Step(`^the kept span "([^"]*)" has the trace id of span "([^"]*)"$`, func(ctx context.Context, name, parent string) error {
		w := world(ctx).Telemetry
		kept, err := keptSpan(w.setup, name)
		if err != nil {
			return err
		}
		ref, err := w.span(parent)
		if err != nil {
			return err
		}
		if kept.TraceID != ref.span.SpanContext().TraceID().String() {
			return fmt.Errorf("span %q trace id %s, want %s", name, kept.TraceID, ref.span.SpanContext().TraceID())
		}
		return nil
	})
	sc.Step(`^the kept span "([^"]*)" has parent span id of span "([^"]*)"$`, func(ctx context.Context, name, parent string) error {
		w := world(ctx).Telemetry
		kept, err := keptSpan(w.setup, name)
		if err != nil {
			return err
		}
		ref, err := w.span(parent)
		if err != nil {
			return err
		}
		if kept.ParentSpanID != ref.span.SpanContext().SpanID().String() {
			return fmt.Errorf("span %q parent %s, want %s", name, kept.ParentSpanID, ref.span.SpanContext().SpanID())
		}
		return nil
	})
	sc.Step(`^the kept span "([^"]*)" has parent span id of span "([^"]*)", name "([^"]*)", status "([^"]*)", run id "([^"]*)" and tenant "([^"]*)"$`, func(ctx context.Context, name, parent, wantName, status, run, tenant string) error {
		w := world(ctx).Telemetry
		kept, err := keptSpan(w.setup, name)
		if err != nil {
			return err
		}
		ref, err := w.span(parent)
		if err != nil {
			return err
		}
		if kept.ParentSpanID != ref.span.SpanContext().SpanID().String() {
			return fmt.Errorf("parent = %s, want %s", kept.ParentSpanID, ref.span.SpanContext().SpanID())
		}
		if kept.Name != wantName || kept.Status != status || kept.RunID != run || kept.Tenant != tenant {
			return fmt.Errorf("kept span = %+v, want name %s status %s run %s tenant %s", kept, wantName, status, run, tenant)
		}
		return nil
	})
	sc.Step(`^the kept span "([^"]*)" starts at "([^"]*)" and ends at "([^"]*)"$`, func(ctx context.Context, name, start, end string) error {
		kept, err := keptSpan(world(ctx).Telemetry.setup, name)
		if err != nil {
			return err
		}
		return compareTimes(kept.Start, kept.End, start, end)
	})
	sc.Step(`^the kept span "([^"]*)" has attribute "([^"]*)" with the value (.*)$`, func(ctx context.Context, name, key, value string) error {
		kept, err := keptSpan(world(ctx).Telemetry.setup, name)
		if err != nil {
			return err
		}
		got, ok := attrAt(kept.Attributes, key)
		if !ok {
			return fmt.Errorf("span %q has no attribute %q", name, key)
		}
		if !valueEqual(got, parseValue(value)) {
			return fmt.Errorf("span attribute %q = %v, want %v", key, got, parseValue(value))
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" reads the spans of run "([^"]*)"$`, func(ctx context.Context, tenant, run string) error {
		w := world(ctx).Telemetry
		spans, err := w.readSpans(tenant, run)
		w.lastRead, w.lastErr = spans, err
		return nil
	})
	sc.Step(`^tenant "([^"]*)" reads the spans named (.*) in that order$`, func(ctx context.Context, tenant, names string) error {
		w := world(ctx).Telemetry
		spans, err := w.readSpans(tenant, w.run)
		w.lastRead, w.lastErr = spans, err
		return compareSpanNames(spans, err, parseQuotedNames(names))
	})
	sc.Step(`^tenant "([^"]*)" reads the spans in span id order$`, func(ctx context.Context, tenant string) error {
		w := world(ctx).Telemetry
		spans, err := w.readSpans(tenant, w.run)
		w.lastRead, w.lastErr = spans, err
		if err != nil {
			return err
		}
		for i := 1; i < len(spans); i++ {
			if spans[i-1].SpanID > spans[i].SpanID {
				return fmt.Errorf("spans are not in span id order: %s before %s", spans[i-1].SpanID, spans[i].SpanID)
			}
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" reads (\d+) spans? named (.*) in that order$`, func(ctx context.Context, tenant string, count int, names string) error {
		w := world(ctx).Telemetry
		spans, err := w.readSpans(tenant, w.run)
		w.lastRead, w.lastErr = spans, err
		want := parseQuotedNames(names)
		if len(want) != count {
			return fmt.Errorf("step names %d spans but says %d", len(want), count)
		}
		return compareSpanNames(spans, err, want)
	})
	sc.Step(`^tenant "([^"]*)" reads (\d+) spans? of run "([^"]*)"$`, func(ctx context.Context, tenant string, count int, run string) error {
		w := world(ctx).Telemetry
		spans, err := w.readSpans(tenant, run)
		w.lastRead, w.lastErr = spans, err
		if err != nil {
			return err
		}
		if len(spans) != count {
			return fmt.Errorf("read %d spans, want %d", len(spans), count)
		}
		return nil
	})
	sc.Step(`^the read returns (\d+) spans? and fails with code "([^"]*)"$`, func(ctx context.Context, count int, code string) error {
		w := world(ctx).Telemetry
		if len(w.lastRead) != count {
			return fmt.Errorf("read %d spans, want %d", len(w.lastRead), count)
		}
		if w.lastErr == nil {
			return fmt.Errorf("read succeeded, want code %s", code)
		}
		if got := core.CodeOf(w.lastErr); string(got) != code {
			return fmt.Errorf("read failed with %s, want %s", got, code)
		}
		return nil
	})

	sc.Step(`^span "([^"]*)" ends with status "([^"]*)"$`, func(ctx context.Context, name, status string) error {
		kept, err := keptSpan(world(ctx).Telemetry.setup, name)
		if err != nil {
			return err
		}
		if kept.Status != status {
			return fmt.Errorf("span %q status = %s, want %s", name, kept.Status, status)
		}
		return nil
	})
}
