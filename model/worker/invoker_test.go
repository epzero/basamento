package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

func TestInvokerStartCarriesTheWholeRequestAndRunSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	declare(t, s, "p", pool.Token, "m")

	seed := int64(42)
	req := model.Request{
		Model:        "m",
		Op:           model.CapChat,
		Messages:     json.RawMessage(`[{"role":"user","content":"hi"}]`),
		Tools:        json.RawMessage(`[{"name":"t"}]`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
		Text:         map[string]string{"prompt": "hello"},
		Params:       json.RawMessage(`{"steps":4}`),
		Seed:         &seed,
		ClientRef:    "r1",
	}
	runCtx := core.WithTenant(WithStartOptions(ctx, StartOptions{
		Class: ClassInteractive, QueueTimeout: 2 * time.Minute,
		MaxRuntime: time.Hour, ReservedMpts: 7, TraceParent: "trace-1",
	}), "acme")
	inv := &Invoker{Store: s, Pool: "p"}
	if _, err := inv.Start(runCtx, req); err != nil {
		t.Fatal(err)
	}
	got := state(t, s, "r1")
	if got.Class != ClassInteractive || got.Units != 1 || got.QueueTimeout != 2*time.Minute ||
		got.MaxRuntime != time.Hour || got.ReservedMpts != 7 || got.TraceParent != "trace-1" {
		t.Fatalf("queued %+v, want the run's class, timeout, reservation and trace", got)
	}
	var stored model.Request
	if err := json.Unmarshal(got.Request, &stored); err != nil {
		t.Fatalf("request payload is not a model.Request: %v", err)
	}
	if stored.Op != model.CapChat || string(stored.Messages) != string(req.Messages) ||
		string(stored.Params) != string(req.Params) || stored.Seed == nil || *stored.Seed != seed ||
		stored.Text["prompt"] != "hello" {
		t.Fatalf("stored request %+v, want the whole request", stored)
	}
}

func TestInvokerStartUsesThePoolDescriptorsUnits(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	pool := mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	declareDescriptor(t, s, "p", pool.Token, descriptor("m", 4, false))
	inv := &Invoker{Store: s, Pool: "p"}
	if _, err := inv.Start(ctx, model.Request{Model: "m", ClientRef: "r1"}); err != nil {
		t.Fatal(err)
	}
	if got := state(t, s, "r1"); got.Units != 4 {
		t.Fatalf("units %d, want the descriptor's 4", got.Units)
	}
}

func TestInvokerStartRefusesAModelThePoolDoesNotServe(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	mustPool(t, s, PoolSpec{ID: "p", Tenant: "acme"})
	inv := &Invoker{Store: s, Pool: "p"}
	_, err := inv.Start(ctx, model.Request{Model: "missing", ClientRef: "r1"})
	if core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("got %v, want not_found", err)
	}
}

func declareDescriptor(t *testing.T, s *Store, poolID, token string, d Descriptor) {
	t.Helper()
	w, err := s.Join(context.Background(), JoinRequest{PoolToken: token, ProtocolVersion: ProtocolVersion,
		Descriptors: []Descriptor{d}, Capacity: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Leave(context.Background(), w.ID, w.Credential); err != nil {
		t.Fatal(err)
	}
}

func TestInvokerScopesHandlesToTheStartingTenant(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	pool := mustPool(t, s, PoolSpec{ID: "shared"})
	declareDescriptor(t, s, "shared", pool.Token, descriptor("m", 1, false))
	inv := &Invoker{Store: s, Pool: "shared"}
	acme := core.WithTenant(ctx, "acme")
	h, err := inv.Start(acme, model.Request{Model: "m", ClientRef: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	globex := core.WithTenant(ctx, "globex")
	if got, err := inv.Lookup(globex, "r1"); err != nil || got != nil {
		t.Fatalf("globex lookup = %+v, %v; want not found", got, err)
	}
	if _, err := inv.Result(globex, h); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("globex result = %v, want not_found", err)
	}
	if _, err := inv.Events(globex, h, 0); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("globex events = %v, want not_found", err)
	}
	if err := inv.Cancel(globex, h); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("globex cancel = %v, want not_found", err)
	}
	if _, err := inv.Start(globex, model.Request{Model: "m", ClientRef: "r1"}); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("globex start reusing acme's ref = %v, want not_found", err)
	}
	if err := inv.Cancel(acme, h); err != nil {
		t.Fatalf("acme cancel: %v", err)
	}
}
