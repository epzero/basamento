package tool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/tool"
)

// memBlob is an in-memory platform.Blob for the layer tests.
type memBlob struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemBlob() *memBlob { return &memBlob{data: map[string][]byte{}} }

func (b *memBlob) Put(_ context.Context, key string, r io.Reader, _ string) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data[key] = raw
	return nil
}

func (b *memBlob) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, ok := b.data[key]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "no blob "+key, nil)
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (b *memBlob) Exists(_ context.Context, key string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.data[key]
	return ok, nil
}

func (b *memBlob) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.data, key)
	return nil
}

func (b *memBlob) SignedURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "mem://" + key, nil
}

func (b *memBlob) StartUpload(context.Context, platform.UploadOptions) (platform.Upload, error) {
	return platform.Upload{}, core.NewError(core.CodeInvalidArgument, "layer tests do not stage uploads", nil)
}

func (b *memBlob) Commit(context.Context, string) (string, error) {
	return "", core.NewError(core.CodeInvalidArgument, "layer tests do not stage uploads", nil)
}

func (b *memBlob) len(key string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.data[key])
}

func schemaOf(property, typ string, required bool, closed bool) json.RawMessage {
	prop := map[string]any{}
	if typ != "" {
		prop["type"] = typ
	}
	doc := map[string]any{
		"type":       "object",
		"properties": map[string]any{property: prop},
	}
	if required {
		doc["required"] = []string{property}
	}
	if closed {
		doc["additionalProperties"] = false
	}
	raw, _ := json.Marshal(doc)
	return raw
}

func layerWith(clock tool.Clock, blob *memBlob, log *slog.Logger) *tool.Layer {
	return tool.New(tool.Config{Clock: clock, Blob: blob, Log: log})
}

func fixedClock() *coretest.FakeClock {
	return coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
}

func TestStepRefusesToolOutsideAllowlist(t *testing.T) {
	ran := false
	other := tool.Tool{Name: "delete_file", Run: func(context.Context, json.RawMessage) (tool.Result, error) {
		ran = true
		return tool.Result{}, nil
	}}
	set := &tool.Set{Tools: []tool.Tool{other}}
	out, err := layerWith(fixedClock(), newMemBlob(), nil).Step(context.Background(), set,
		tool.Call{Name: "search", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if !out.Failed || out.Code != core.CodePermissionDenied {
		t.Fatalf("outcome %+v, want permission_denied", out)
	}
	if ran {
		t.Fatal("a refused tool ran")
	}
	if !strings.Contains(out.Text(), "delete_file") && !strings.Contains(out.Text(), "search") {
		t.Fatalf("refusal does not name the tool: %q", out.Text())
	}
}

func TestStepValidatesArguments(t *testing.T) {
	tests := []struct {
		name  string
		input json.RawMessage
		args  string
		want  []string
	}{
		{
			name:  "required",
			input: schemaOf("tracking_number", "", true, false),
			args:  `{}`,
			want:  []string{`"tracking_number"`, "required"},
		},
		{
			name:  "type",
			input: schemaOf("tracking_number", "string", true, true),
			args:  `{"tracking_number": 42}`,
			want:  []string{`"tracking_number"`, "must be a string"},
		},
		{
			name:  "additional",
			input: schemaOf("tracking_number", "string", true, true),
			args:  `{"tracking_number": "P-1", "mode": "air"}`,
			want:  []string{`"mode"`, "not allowed"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			set := &tool.Set{Tools: []tool.Tool{{
				Name:  "track-parcel",
				Input: tc.input,
				Run: func(context.Context, json.RawMessage) (tool.Result, error) {
					ran = true
					return tool.Result{}, nil
				},
			}}}
			out, err := layerWith(fixedClock(), newMemBlob(), nil).Step(context.Background(), set,
				tool.Call{Name: "track-parcel", Args: json.RawMessage(tc.args)})
			if err != nil {
				t.Fatalf("Step: %v", err)
			}
			if !out.Failed || out.Code != core.CodeInvalidArgument {
				t.Fatalf("outcome %+v, want invalid_argument", out)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.Message, want) {
					t.Errorf("message %q does not contain %q", out.Message, want)
				}
			}
			if ran {
				t.Fatal("a tool with invalid arguments ran")
			}
		})
	}
}

func TestStepRunsToolAndReturnsResult(t *testing.T) {
	set := &tool.Set{Tools: []tool.Tool{{
		Name:  "search",
		Input: schemaOf("q", "string", true, false),
		Run: func(_ context.Context, args json.RawMessage) (tool.Result, error) {
			return tool.Result{Content: args}, nil
		},
	}}}
	out, err := layerWith(fixedClock(), newMemBlob(), nil).Step(context.Background(), set,
		tool.Call{Name: "search", Args: json.RawMessage(`{"q":"x"}`)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.Failed {
		t.Fatalf("outcome failed: %+v", out)
	}
	if string(out.Result.Content) != `{"q":"x"}` {
		t.Fatalf("result %s", out.Result.Content)
	}
}

func TestStepInvalidSchemaIsInternal(t *testing.T) {
	set := &tool.Set{Tools: []tool.Tool{{
		Name:  "broken",
		Input: json.RawMessage(`{"type": "nonsense"}`),
		Run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{}, nil
		},
	}}}
	out, err := layerWith(fixedClock(), newMemBlob(), nil).Step(context.Background(), set,
		tool.Call{Name: "broken", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if !out.Failed || out.Code != core.CodeInternal {
		t.Fatalf("outcome %+v, want internal", out)
	}
}

func TestStepOversizedResultWithoutBlobFailsTheLayer(t *testing.T) {
	set := &tool.Set{Tools: []tool.Tool{{
		Name:      "dump",
		MaxResult: 8,
		Run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{Content: json.RawMessage(`"0123456789"`)}, nil
		},
	}}}
	_, err := tool.New(tool.Config{Clock: fixedClock()}).Step(context.Background(), set,
		tool.Call{Name: "dump", Args: json.RawMessage(`{}`)})
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("error %v, want internal", err)
	}
}

func TestStepMapsToolErrorWithoutDetails(t *testing.T) {
	set := &tool.Set{Tools: []tool.Tool{{
		Name: "search",
		Run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{}, core.NewError(core.CodeVendor, "upstream index is rebuilding",
				map[string]any{"host": "index-7.internal"})
		},
	}}}
	out, err := layerWith(fixedClock(), newMemBlob(), nil).Step(context.Background(), set,
		tool.Call{Name: "search", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if !out.Failed || out.Code != core.CodeVendor || out.Message != "upstream index is rebuilding" {
		t.Fatalf("outcome %+v", out)
	}
	if strings.Contains(out.Text(), "index-7.internal") {
		t.Fatalf("details reached the model: %q", out.Text())
	}
}

func TestStepRecoversPanic(t *testing.T) {
	var logBuf bytes.Buffer
	set := &tool.Set{Tools: []tool.Tool{{
		Name: "crawl",
		Run: func(context.Context, json.RawMessage) (tool.Result, error) {
			panic("boom-7")
		},
	}}}
	out, err := layerWith(fixedClock(), newMemBlob(), slog.New(slog.NewTextHandler(&logBuf, nil))).
		Step(context.Background(), set, tool.Call{Name: "crawl", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if !out.Failed || out.Code != core.CodeInternal {
		t.Fatalf("outcome %+v, want internal", out)
	}
	if !strings.Contains(out.Message, "crawl") {
		t.Fatalf("message %q does not name the tool", out.Message)
	}
	if strings.Contains(out.Message, "boom-7") {
		t.Fatalf("panic value reached the outcome: %q", out.Message)
	}
	if !strings.Contains(logBuf.String(), "boom-7") {
		t.Fatalf("panic value did not reach the log: %q", logBuf.String())
	}
}

func TestStepTimesOutAtTheToolsLimit(t *testing.T) {
	clock := fixedClock()
	started := make(chan struct{})
	set := &tool.Set{Tools: []tool.Tool{{
		Name:    "slow-report",
		Timeout: 30 * time.Second,
		Run: func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
			close(started)
			<-ctx.Done()
			return tool.Result{}, ctx.Err()
		},
	}}}
	type done struct {
		out tool.Outcome
		err error
	}
	result := make(chan done, 1)
	go func() {
		out, err := layerWith(clock, newMemBlob(), nil).Step(context.Background(), set,
			tool.Call{Name: "slow-report", Args: json.RawMessage(`{}`)})
		result <- done{out, err}
	}()
	<-started
	clock.Advance(30 * time.Second)
	got := <-result
	if got.err != nil {
		t.Fatalf("Step: %v", got.err)
	}
	if !got.out.Failed || got.out.Code != core.CodeTimeout {
		t.Fatalf("outcome %+v, want timeout", got.out)
	}
}

func TestStepCancelsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	set := &tool.Set{Tools: []tool.Tool{{
		Name: "search",
		Run: func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
			close(started)
			<-ctx.Done()
			return tool.Result{}, ctx.Err()
		},
	}}}
	done := make(chan error, 1)
	go func() {
		_, err := layerWith(fixedClock(), newMemBlob(), nil).Step(ctx, set,
			tool.Call{Name: "search", Args: json.RawMessage(`{}`)})
		done <- err
	}()
	<-started
	cancel()
	err := <-done
	if err == nil || core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("error %v, want cancelled", err)
	}
}

func TestStepSpillsAnOversizedResult(t *testing.T) {
	blob := newMemBlob()
	content := json.RawMessage(`"` + strings.Repeat("a", 2046) + `"`)
	set := &tool.Set{Tools: []tool.Tool{{
		Name:      "dump",
		MaxResult: 1024,
		Run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{Content: content}, nil
		},
	}}}
	out, err := layerWith(fixedClock(), blob, nil).Step(context.Background(), set,
		tool.Call{Name: "dump", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.Failed {
		t.Fatalf("outcome %+v", out)
	}
	if out.Result.BlobRef == "" || out.Result.Content != nil {
		t.Fatalf("result %+v, want only a blob reference", out.Result)
	}
	if got := blob.len(out.Result.BlobRef); got != len(content) {
		t.Fatalf("blob holds %d bytes, want %d", got, len(content))
	}
}

func TestStepKeepsAResultAtTheLimit(t *testing.T) {
	content := json.RawMessage(`"` + strings.Repeat("a", 1022) + `"`)
	blob := newMemBlob()
	set := &tool.Set{Tools: []tool.Tool{{
		Name:      "dump",
		MaxResult: 1024,
		Run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{Content: content}, nil
		},
	}}}
	out, err := layerWith(fixedClock(), blob, nil).Step(context.Background(), set,
		tool.Call{Name: "dump", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.Result.BlobRef != "" || string(out.Result.Content) != string(content) {
		t.Fatalf("result %+v, want the bytes inline", out.Result)
	}
}

func TestStepReplaysThroughTheCallerJournal(t *testing.T) {
	calls := 0
	set := &tool.Set{Tools: []tool.Tool{{
		Name: "place-order",
		Run: func(context.Context, json.RawMessage) (tool.Result, error) {
			calls++
			return tool.Result{Content: json.RawMessage(`{"order_id":"O-1"}`)}, nil
		},
	}}}
	layer := layerWith(fixedClock(), newMemBlob(), nil)
	first, err := layer.Step(context.Background(), set, tool.Call{Name: "place-order"})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	// A resumed run receives the recorded outcome instead of calling again.
	recorded, _ := json.Marshal(first)
	var replay tool.Outcome
	if err := json.Unmarshal(recorded, &replay); err != nil {
		t.Fatalf("unmarshal recorded outcome: %v", err)
	}
	if string(replay.Result.Content) != `{"order_id":"O-1"}` {
		t.Fatalf("replayed result %s", replay.Result.Content)
	}
	if calls != 1 {
		t.Fatalf("tool ran %d times, want 1", calls)
	}
}

func TestKeyIsStableAndDistinct(t *testing.T) {
	a := tool.Key("run_1", "worker", 1, 1)
	if a != "tool:run_1:worker:1:1" {
		t.Fatalf("key %q", a)
	}
	if a != tool.Key("run_1", "worker", 1, 1) {
		t.Fatal("key is not stable")
	}
	if a == tool.Key("run_1", "worker", 1, 2) {
		t.Fatal("two calls share a key")
	}
	if a == tool.Key("run_1", "worker", 2, 1) {
		t.Fatal("two visits share a key")
	}
	if a == tool.Key("run_2", "worker", 1, 1) {
		t.Fatal("two runs share a key")
	}
}

func TestRunStepUsesTheSuppliedClock(t *testing.T) {
	clock := fixedClock()
	set := &tool.Set{Tools: []tool.Tool{{
		Name: "search",
		Run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{Content: json.RawMessage(`"ok"`)}, nil
		},
	}}}
	out, err := tool.RunStep(context.Background(), set, tool.Call{Name: "search"}, clock.Now)
	if err != nil || out.Failed {
		t.Fatalf("RunStep: %+v %v", out, err)
	}
}

func TestSetGetAndNames(t *testing.T) {
	set := &tool.Set{Tools: []tool.Tool{{Name: "fetch"}, {Name: "search"}}}
	if _, ok := set.Get("search"); !ok {
		t.Fatal("Get(search) missed")
	}
	if _, ok := set.Get("delete_file"); ok {
		t.Fatal("Get(delete_file) found a tool outside the set")
	}
	if got := strings.Join(set.Names(), ","); got != "fetch,search" {
		t.Fatalf("names %q", got)
	}
	var missing *tool.Set
	if _, ok := missing.Get("search"); ok {
		t.Fatal("a nil set found a tool")
	}
	if missing.Names() != nil {
		t.Fatal("a nil set has names")
	}
}
