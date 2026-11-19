package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
)

// MetricPoint is one recorded measurement. Labels are redacted before the
// point is kept or exported (REQ-TELEM-006, REQ-TELEM-007).
type MetricPoint struct {
	Name   string
	Kind   string
	Value  float64
	IsInt  bool
	Time   time.Time
	Labels map[string]any
}

// Instrument kinds kept on a MetricPoint.
const (
	KindCounter   = "counter"
	KindUpDown    = "updowncounter"
	KindHistogram = "histogram"
	KindGauge     = "gauge"
)

// meterProvider is the metric.MeterProvider of one Setup.
type meterProvider struct {
	embedded.MeterProvider
	setup *Setup
}

func (p *meterProvider) Meter(string, ...metric.MeterOption) metric.Meter {
	return &meter{setup: p.setup}
}

// meter records every synchronous measurement directly into the setup so the
// embedded profile keeps points and the cloud profile can export them. It does
// not aggregate: one call is one point.
type meter struct {
	embedded.Meter
	setup *Setup
}

func (m *meter) newInstrument(name, kind string) instrument {
	return instrument{setup: m.setup, name: name, kind: kind}
}

func (m *meter) Int64Counter(name string, _ ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return &int64Sync{instrument: m.newInstrument(name, KindCounter)}, nil
}

func (m *meter) Int64UpDownCounter(name string, _ ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	return &int64Sync{instrument: m.newInstrument(name, KindUpDown)}, nil
}

func (m *meter) Int64Histogram(name string, _ ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	return &int64Sync{instrument: m.newInstrument(name, KindHistogram)}, nil
}

func (m *meter) Int64Gauge(name string, _ ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	return &int64Sync{instrument: m.newInstrument(name, KindGauge)}, nil
}

func (m *meter) Float64Counter(name string, _ ...metric.Float64CounterOption) (metric.Float64Counter, error) {
	return &float64Sync{instrument: m.newInstrument(name, KindCounter)}, nil
}

func (m *meter) Float64UpDownCounter(name string, _ ...metric.Float64UpDownCounterOption) (metric.Float64UpDownCounter, error) {
	return &float64Sync{instrument: m.newInstrument(name, KindUpDown)}, nil
}

func (m *meter) Float64Histogram(name string, _ ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return &float64Sync{instrument: m.newInstrument(name, KindHistogram)}, nil
}

func (m *meter) Float64Gauge(name string, _ ...metric.Float64GaugeOption) (metric.Float64Gauge, error) {
	return &float64Sync{instrument: m.newInstrument(name, KindGauge)}, nil
}

func (m *meter) Int64ObservableCounter(name string, _ ...metric.Int64ObservableCounterOption) (metric.Int64ObservableCounter, error) {
	return &int64Observable{name: name}, nil
}

func (m *meter) Int64ObservableUpDownCounter(name string, _ ...metric.Int64ObservableUpDownCounterOption) (metric.Int64ObservableUpDownCounter, error) {
	return &int64Observable{name: name}, nil
}

func (m *meter) Int64ObservableGauge(name string, _ ...metric.Int64ObservableGaugeOption) (metric.Int64ObservableGauge, error) {
	return &int64Observable{name: name}, nil
}

func (m *meter) Float64ObservableCounter(name string, _ ...metric.Float64ObservableCounterOption) (metric.Float64ObservableCounter, error) {
	return &float64Observable{name: name}, nil
}

func (m *meter) Float64ObservableUpDownCounter(name string, _ ...metric.Float64ObservableUpDownCounterOption) (metric.Float64ObservableUpDownCounter, error) {
	return &float64Observable{name: name}, nil
}

func (m *meter) Float64ObservableGauge(name string, _ ...metric.Float64ObservableGaugeOption) (metric.Float64ObservableGauge, error) {
	return &float64Observable{name: name}, nil
}

func (m *meter) RegisterCallback(metric.Callback, ...metric.Observable) (metric.Registration, error) {
	return noopRegistration{}, nil
}

type instrument struct {
	setup *Setup
	name  string
	kind  string
}

func (i instrument) enabled() bool { return !i.setup.isClosed() }

func (i instrument) record(ctx context.Context, value float64, isInt bool, labels map[string]any) {
	if !i.enabled() {
		return
	}
	i.setup.recordMetric(MetricPoint{
		Name:   i.name,
		Kind:   i.kind,
		Value:  value,
		IsInt:  isInt,
		Time:   i.setup.clock.Now(),
		Labels: i.setup.redact.Map(labels),
	})
}

// int64Sync serves the counter, up-down counter, histogram and gauge interfaces.
type int64Sync struct {
	embedded.Int64Counter
	embedded.Int64UpDownCounter
	embedded.Int64Histogram
	embedded.Int64Gauge
	instrument
}

func (s *int64Sync) Add(ctx context.Context, incr int64, options ...metric.AddOption) {
	s.record(ctx, float64(incr), true, addLabels(options))
}

func (s *int64Sync) Record(ctx context.Context, value int64, options ...metric.RecordOption) {
	s.record(ctx, float64(value), true, recordLabels(options))
}

func (s *int64Sync) Enabled(context.Context) bool { return s.enabled() }

// float64Sync serves the counter, up-down counter, histogram and gauge interfaces.
type float64Sync struct {
	embedded.Float64Counter
	embedded.Float64UpDownCounter
	embedded.Float64Histogram
	embedded.Float64Gauge
	instrument
}

func (s *float64Sync) Add(ctx context.Context, incr float64, options ...metric.AddOption) {
	s.record(ctx, incr, false, addLabels(options))
}

func (s *float64Sync) Record(ctx context.Context, value float64, options ...metric.RecordOption) {
	s.record(ctx, value, false, recordLabels(options))
}

func (s *float64Sync) Enabled(context.Context) bool { return s.enabled() }

// int64Observable satisfies the marker interfaces of the asynchronous int64
// instruments. This setup has no collection cycle, so a callback registered on
// it never runs.
type int64Observable struct {
	embedded.Int64ObservableCounter
	embedded.Int64ObservableUpDownCounter
	embedded.Int64ObservableGauge
	metric.Int64Observable
	name string
}

// float64Observable satisfies the marker interfaces of the asynchronous float64
// instruments.
type float64Observable struct {
	embedded.Float64ObservableCounter
	embedded.Float64ObservableUpDownCounter
	embedded.Float64ObservableGauge
	metric.Float64Observable
	name string
}

type noopRegistration struct{ embedded.Registration }

func (noopRegistration) Unregister() error { return nil }

func addLabels(options []metric.AddOption) map[string]any {
	set := metric.NewAddConfig(options).Attributes()
	return labelMap(set.ToSlice())
}

func recordLabels(options []metric.RecordOption) map[string]any {
	set := metric.NewRecordConfig(options).Attributes()
	return labelMap(set.ToSlice())
}

func labelMap(attrs []attribute.KeyValue) map[string]any {
	out := make(map[string]any, len(attrs))
	for _, kv := range attrs {
		out[string(kv.Key)] = kv.Value.AsInterface()
	}
	return out
}
