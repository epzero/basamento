package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

func registerTelemetryExport(sc *godog.ScenarioContext) {
	sc.Step(`^a log record is written inside a span "([^"]*)" of run "([^"]*)"$`, func(ctx context.Context, name, run string) error {
		w := world(ctx).Telemetry
		spanCtx := w.startSpan(w.ctxFor(w.tenant, run), name)
		w.setup.Logger().InfoContext(spanCtx, "inside")
		ref, _ := w.span(name)
		ref.span.End()
		return nil
	})
	sc.Step(`^a log record is written outside any span$`, func(ctx context.Context) error {
		world(ctx).Telemetry.setup.Logger().InfoContext(world(ctx).Telemetry.currentCtx(), "outside")
		return nil
	})
	sc.Step(`^a log record carrying "([^"]*)" is written$`, func(ctx context.Context, message string) error {
		world(ctx).Telemetry.setup.Logger().InfoContext(world(ctx).Telemetry.currentCtx(), message)
		return nil
	})
	sc.Step(`^a log record "([^"]*)" is written and metric "([^"]*)" is recorded with the value (-?\d+)$`, func(ctx context.Context, message, name string, value int64) error {
		w := world(ctx).Telemetry
		w.markRecording()
		w.setup.Logger().InfoContext(w.currentCtx(), message)
		err := w.metric(w.currentCtx(), name, value)
		w.markRecorded()
		w.lastRecord = err
		return err
	})
	sc.Step(`^metric "([^"]*)" is recorded with the value (-?\d+) and a log record "([^"]*)" is written$`, func(ctx context.Context, name string, value int64, message string) error {
		w := world(ctx).Telemetry
		w.markRecording()
		err := w.metric(w.currentCtx(), name, value)
		w.setup.Logger().InfoContext(w.currentCtx(), message)
		w.markRecorded()
		w.lastRecord = err
		return err
	})

	sc.Step(`^a span "([^"]*)" is started and ended, a log record "([^"]*)" is written, and metric "([^"]*)" is recorded with the value (-?\d+)$`, func(ctx context.Context, spanName, message, metricName string, value int64) error {
		return world(ctx).Telemetry.recordEverything(spanName, message, metricName, value)
	})
	sc.Step(`^span "([^"]*)" is started and ended, a log record "([^"]*)" is written, and metric "([^"]*)" is recorded with the value (-?\d+)$`, func(ctx context.Context, spanName, message, metricName string, value int64) error {
		return world(ctx).Telemetry.recordEverything(spanName, message, metricName, value)
	})

	sc.Step(`^the telemetry setup holds (\d+) spans? "([^"]*)" for run "([^"]*)"$`, func(ctx context.Context, count int, name, run string) error {
		n := 0
		for _, span := range world(ctx).Telemetry.setup.Spans() {
			if span.Name == name && span.RunID == run {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("held %d spans %q for run %q, want %d", n, name, run, count)
		}
		return nil
	})
	sc.Step(`^the telemetry setup holds (\d+) log records? with message "([^"]*)"$`, func(ctx context.Context, count int, message string) error {
		n := 0
		for _, rec := range world(ctx).Telemetry.setup.Logs() {
			if rec.Message == message {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("held %d log records with message %q, want %d", n, message, count)
		}
		return nil
	})
	sc.Step(`^the telemetry setup holds (\d+) metric points? named "([^"]*)" with the value (-?\d+)$`, func(ctx context.Context, count int, name string, value int64) error {
		n := 0
		for _, point := range world(ctx).Telemetry.setup.Metrics() {
			if point.Name == name && int64(point.Value) == value {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("held %d metric points %q with value %d, want %d", n, name, value, count)
		}
		return nil
	})
	sc.Step(`^the log record has the trace id and span id of span "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		ref, err := w.span(name)
		if err != nil {
			return err
		}
		rec, err := lastLog(w.setup)
		if err != nil {
			return err
		}
		sc := ref.span.SpanContext()
		if rec.Attrs["trace_id"] != sc.TraceID().String() || rec.Attrs["span_id"] != sc.SpanID().String() {
			return fmt.Errorf("log trace/span = %v/%v, want %s/%s", rec.Attrs["trace_id"], rec.Attrs["span_id"], sc.TraceID(), sc.SpanID())
		}
		return nil
	})
	sc.Step(`^the log record has attribute "([^"]*)" with the value of run "([^"]*)"$`, func(ctx context.Context, key, run string) error {
		rec, err := lastLog(world(ctx).Telemetry.setup)
		if err != nil {
			return err
		}
		if rec.Attrs[key] != run {
			return fmt.Errorf("log attribute %q = %v, want %q", key, rec.Attrs[key], run)
		}
		return nil
	})
	sc.Step(`^the log record has attribute "([^"]*)" with the value "([^"]*)"$`, func(ctx context.Context, key, value string) error {
		rec, err := lastLog(world(ctx).Telemetry.setup)
		if err != nil {
			return err
		}
		if rec.Attrs[key] != value {
			return fmt.Errorf("log attribute %q = %v, want %q", key, rec.Attrs[key], value)
		}
		return nil
	})
	sc.Step(`^the log record has attribute "([^"]*)" with the empty value$`, func(ctx context.Context, key string) error {
		rec, err := lastLog(world(ctx).Telemetry.setup)
		if err != nil {
			return err
		}
		value, ok := rec.Attrs[key]
		if !ok || value != "" {
			return fmt.Errorf("log attribute %q = %v (present %v), want empty", key, value, ok)
		}
		return nil
	})

	sc.Step(`^the telemetry setup is flushed$`, func(ctx context.Context) error {
		return world(ctx).Telemetry.setup.Flush(ctx)
	})
	sc.Step(`^the telemetry setup makes (\d+) export attempts?$`, func(ctx context.Context, count int) error {
		w := world(ctx).Telemetry
		for i := 0; i < count; i++ {
			w.lastErr = w.setup.ExportOnce(ctx)
		}
		return nil
	})
	sc.Step(`^the simulated collector answers after (\d+) seconds? of clock time$`, func(ctx context.Context, _ int) error {
		world(ctx).Telemetry.collector.SetSlow()
		return nil
	})
	sc.Step(`^the simulated collector is unreachable$`, func(ctx context.Context) error {
		world(ctx).Telemetry.collector.SetUnreachable(true)
		return nil
	})
	sc.Step(`^the simulated collector becomes reachable$`, func(ctx context.Context) error {
		world(ctx).Telemetry.collector.SetUnreachable(false)
		return nil
	})
	sc.Step(`^recording returns without the clock advancing$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		if !w.before.Equal(w.after) {
			return fmt.Errorf("recording advanced the clock from %v to %v", w.before, w.after)
		}
		return nil
	})
	sc.Step(`^the collector has received (\d+) spans?$`, func(ctx context.Context, count int) error {
		if got := len(world(ctx).Telemetry.collector.Spans()); got != count {
			return fmt.Errorf("collector has %d spans, want %d", got, count)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) spans? named "([^"]*)"(?: over OTLP)?$`, func(ctx context.Context, count int, name string) error {
		n := 0
		for _, span := range world(ctx).Telemetry.collector.Spans() {
			if span.Name == name {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("collector received %d spans named %q, want %d", n, name, count)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) log records? with message "([^"]*)"(?: over OTLP)?$`, func(ctx context.Context, count int, message string) error {
		n := 0
		for _, rec := range world(ctx).Telemetry.collector.Logs() {
			if rec.Message == message {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("collector received %d log records with message %q, want %d", n, message, count)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) metric points? named "([^"]*)" with the value (-?\d+)(?: over OTLP)?$`, func(ctx context.Context, count int, name string, value int64) error {
		n := 0
		for _, point := range world(ctx).Telemetry.collector.Metrics() {
			if point.Name == name && int64(point.Value) == value {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("collector received %d metric points %q with value %d, want %d", n, name, value, count)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) log records? with message "([^"]*)" and (\d+) metric points? named "([^"]*)"$`, func(ctx context.Context, logCount int, message string, metricCount int, name string) error {
		collector := world(ctx).Telemetry.collector
		logs := 0
		for _, rec := range collector.Logs() {
			if rec.Message == message {
				logs++
			}
		}
		metrics := 0
		for _, point := range collector.Metrics() {
			if point.Name == name {
				metrics++
			}
		}
		if logs != logCount || metrics != metricCount {
			return fmt.Errorf("collector received %d logs and %d metrics, want %d and %d", logs, metrics, logCount, metricCount)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) spans?, (\d+) log records? and (\d+) metric points?$`, func(ctx context.Context, spans, logs, metrics int) error {
		collector := world(ctx).Telemetry.collector
		gotSpans, gotLogs, gotMetrics := len(collector.Spans()), len(collector.Logs()), len(collector.Metrics())
		if gotSpans != spans || gotLogs != logs || gotMetrics != metrics {
			return fmt.Errorf("collector received %d spans, %d logs, %d metrics; want %d, %d, %d", gotSpans, gotLogs, gotMetrics, spans, logs, metrics)
		}
		return nil
	})
	sc.Step(`^no export batch the collector received contains "([^"]*)"$`, func(ctx context.Context, text string) error {
		if world(ctx).Telemetry.collector.Contains(text) {
			return fmt.Errorf("an export batch contains %q", text)
		}
		return nil
	})
	sc.Step(`^recording the log record and the metric point returns no error$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		if w.lastRecord != nil {
			return fmt.Errorf("recording failed: %w", w.lastRecord)
		}
		return nil
	})
	sc.Step(`^the telemetry setup shuts down$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		err := w.setup.Shutdown(ctx)
		w.shutdownOK = w.collector.Confirmed()
		w.lastErr = err
		return err
	})
	sc.Step(`^the telemetry setup is shut down$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		err := w.setup.Shutdown(ctx)
		w.shutdownOK = w.collector.Confirmed()
		w.lastErr = err
		return err
	})
	sc.Step(`^the telemetry setup has shut down$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		return w.setup.Shutdown(ctx)
	})
	sc.Step(`^the telemetry setup returned only after the collector confirmed it$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		if w.shutdownOK == 0 {
			return fmt.Errorf("shutdown returned without a collector confirmation")
		}
		return nil
	})
	sc.Step(`^the telemetry setup holds (\d+) log records and (\d+) metric points$`, func(ctx context.Context, logs, metrics int) error {
		w := world(ctx).Telemetry
		if got := len(w.setup.Logs()); got != logs {
			return fmt.Errorf("held %d log records, want %d", got, logs)
		}
		if got := len(w.setup.Metrics()); got != metrics {
			return fmt.Errorf("held %d metric points, want %d", got, metrics)
		}
		return nil
	})
}
