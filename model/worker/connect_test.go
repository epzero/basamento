package worker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestWorkerServiceSpeaksConnect guards the transport ADR-0012 names: the
// listener answers a plain HTTP POST carrying the Connect JSON protocol, so a
// worker's generated Connect client reaches it. A gRPC-only server would
// refuse this request.
func TestWorkerServiceSpeaksConnect(t *testing.T) {
	store, _ := testStore(t, time.Unix(1_700_000_000, 0))
	pool, _, err := store.EnsurePoolWithToken(context.Background(),
		PoolSpec{ID: "acme-gpu", Lease: 5 * time.Minute, Grace: time.Minute},
		"0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewService(store).Server(nil)
	defer func() { _ = srv.Stop() }()
	go func() { _ = srv.Serve(ctx, ln) }()

	body := `{"poolToken":"` + pool.Token + `","protocolVersion":"` + ProtocolVersion +
		`","descriptors":[{"id":"sdxl"}],"capacity":4}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+ln.Addr().String()+"/basamento.worker.v1.WorkerService/Join", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join over Connect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("join over Connect = %d %s, want 200", resp.StatusCode, raw)
	}
	var out struct {
		WorkerID   string `json:"workerId"`
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("join response %s: %v", raw, err)
	}
	if out.WorkerID == "" || out.Credential == "" {
		t.Fatalf("join over Connect returned %s, want a worker identity", raw)
	}
}
