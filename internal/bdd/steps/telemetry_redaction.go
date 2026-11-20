package steps

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/cucumber/godog"
	"go.opentelemetry.io/otel/attribute"
)

func registerTelemetryRedaction(sc *godog.ScenarioContext) {
	sc.Step(`^a log record, a span attribute and a metric label each carry the text "([^"]*)"$`, func(ctx context.Context, text string) error {
		w := world(ctx).Telemetry
		w.setup.Logger().InfoContext(w.currentCtx(), text)
		w.startSpan(w.currentCtx(), "redaction")
		ref, _ := w.span("redaction")
		ref.span.SetAttributes(attribute.String("text", text))
		ref.span.End()
		return w.metric(w.currentCtx(), "redaction", 1, attribute.String("text", text))
	})
	sc.Step(`^no kept log record, span attribute or metric label contains "([^"]*)"$`, func(ctx context.Context, text string) error {
		if got := world(ctx).Telemetry.contains(text); got {
			return fmt.Errorf("%q still appears in a kept signal", text)
		}
		return nil
	})
	sc.Step(`^each records "([^"]*)"$`, func(ctx context.Context, want string) error {
		w := world(ctx).Telemetry
		rec, err := lastLog(w.setup)
		if err != nil {
			return err
		}
		span, err := keptSpan(w.setup, "redaction")
		if err != nil {
			return err
		}
		point, err := lastMetric(w.setup, "redaction")
		if err != nil {
			return err
		}
		if rec.Message != want {
			return fmt.Errorf("log message = %q, want %q", rec.Message, want)
		}
		if !valueEqual(span.Attributes["text"], want) {
			return fmt.Errorf("span attribute = %v, want %q", span.Attributes["text"], want)
		}
		if !valueEqual(point.Labels["text"], want) {
			return fmt.Errorf("metric label = %v, want %q", point.Labels["text"], want)
		}
		return nil
	})
	sc.Step(`^a log record, a span attribute and a metric label carry the value "([^"]*)" under the key path "([^"]*)"$`, func(ctx context.Context, value, path string) error {
		w := world(ctx).Telemetry
		w.setup.Logger().InfoContext(w.currentCtx(), "redaction", slog.String(path, value))
		w.startSpan(w.currentCtx(), "redaction")
		ref, _ := w.span("redaction")
		ref.span.SetAttributes(attribute.String(path, value))
		ref.span.End()
		return w.metric(w.currentCtx(), "redaction", 1, attribute.String(path, value))
	})
	sc.Step(`^the log record, the span attribute and the metric label record "(.*)" at "(.*)"$`, func(ctx context.Context, want, path string) error {
		w := world(ctx).Telemetry
		rec, err := lastLog(w.setup)
		if err != nil {
			return err
		}
		span, err := keptSpan(w.setup, "redaction")
		if err != nil {
			return err
		}
		point, err := lastMetric(w.setup, "redaction")
		if err != nil {
			return err
		}
		for _, signal := range []struct {
			name  string
			attrs map[string]any
		}{{"log record", rec.Attrs}, {"span attribute", span.Attributes}, {"metric label", point.Labels}} {
			got, ok := attrAt(signal.attrs, path)
			if !ok {
				return fmt.Errorf("%s has no value at %q", signal.name, path)
			}
			if !valueEqual(got, want) {
				return fmt.Errorf("%s at %q = %v, want %q", signal.name, path, got, want)
			}
		}
		return nil
	})
	sc.Step(`^a log record, a span attribute and a metric label carry (\d+) at "([^"]*)" and "([^"]*)" at "([^"]*)"$`, func(ctx context.Context, number int, numberPath, text, textPath string) error {
		w := world(ctx).Telemetry
		w.setup.Logger().InfoContext(w.currentCtx(), "redaction", slog.Int64(numberPath, int64(number)), slog.String(textPath, text))
		w.startSpan(w.currentCtx(), "redaction")
		ref, _ := w.span("redaction")
		ref.span.SetAttributes(attribute.Int(numberPath, number), attribute.String(textPath, text))
		ref.span.End()
		return w.metric(w.currentCtx(), "redaction", 1, attribute.Int(numberPath, number), attribute.String(textPath, text))
	})
	sc.Step(`^the log record, the span attribute and the metric label record (\d+) at "([^"]*)" and "([^"]*)" at "([^"]*)"$`, func(ctx context.Context, number int, numberPath, text, textPath string) error {
		w := world(ctx).Telemetry
		rec, err := lastLog(w.setup)
		if err != nil {
			return err
		}
		span, err := keptSpan(w.setup, "redaction")
		if err != nil {
			return err
		}
		point, err := lastMetric(w.setup, "redaction")
		if err != nil {
			return err
		}
		for _, signal := range []struct {
			name  string
			attrs map[string]any
		}{{"log record", rec.Attrs}, {"span attribute", span.Attributes}, {"metric label", point.Labels}} {
			gotNumber, ok := attrAt(signal.attrs, numberPath)
			if !ok || !valueEqual(gotNumber, int64(number)) {
				return fmt.Errorf("%s at %q = %v, want %d", signal.name, numberPath, gotNumber, number)
			}
			gotText, ok := attrAt(signal.attrs, textPath)
			if !ok || !valueEqual(gotText, text) {
				return fmt.Errorf("%s at %q = %v, want %q", signal.name, textPath, gotText, text)
			}
		}
		return nil
	})

}
