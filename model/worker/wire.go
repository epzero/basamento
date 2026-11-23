package worker

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	workerv1 "github.com/epzero/basamento/model/worker/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// Service is the platform side of basamento.worker.v1: it serves the short
// calls a worker makes over a listener the platform hosts, and never dials a
// worker (ADR-0012 §4, REQ-MODEL-089). It adapts the contract to the store the
// same way LocalTransport does in-process.
type Service struct {
	workerv1.UnimplementedWorkerServiceServer
	store *Store
	dials atomic.Int64
}

// maxLeaseWait bounds what one Lease long poll may hold a store-backed
// goroutine awake, whatever wait_seconds the worker asks for.
const maxLeaseWait = time.Minute

// maxWorkerStreams bounds concurrent WorkerService calls per connection; a
// worker needs a handful of short calls, and each long poll is bounded by
// maxLeaseWait.
const maxWorkerStreams = 64

// NewService returns the platform side of the worker contract over store.
func NewService(store *Store) *Service { return &Service{store: store} }

// TLSConfig names the PEM files that secure WorkerService. The platform and
// the worker both present a certificate; CAFile verifies the peer. An empty
// CAFile keeps the platform's certificate unchecked by the worker and lets
// the platform accept any client certificate the listener admits, so a
// deployment that leaves it out must stay on loopback.
type TLSConfig struct {
	CertFile string
	KeyFile  string
	CAFile   string
}

// Empty reports whether no TLS file was configured.
func (c TLSConfig) Empty() bool {
	return c.CertFile == "" && c.KeyFile == "" && c.CAFile == ""
}

// ServerCredentials loads the platform side of WorkerService. CertFile and
// KeyFile are required; CAFile makes the platform require and verify the
// worker's certificate (mutual TLS), so a worker without a trusted
// certificate is refused before a call runs.
func (c TLSConfig) ServerCredentials() (credentials.TransportCredentials, error) {
	if c.CertFile == "" || c.KeyFile == "" {
		return nil, core.NewError(core.CodeInvalidArgument,
			"worker TLS needs both --worker.tls.cert-file and --worker.tls.key-file", nil)
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument,
			"worker TLS certificate or key could not be read", map[string]any{"cert": c.CertFile, "key": c.KeyFile})
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if c.CAFile != "" {
		pool, err := loadCertPool(c.CAFile)
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return credentials.NewTLS(cfg), nil
}

// ClientCredentials loads the worker side of WorkerService. CAFile verifies
// the platform's certificate. CertFile and KeyFile are optional together and
// present the worker's certificate; a platform with a CA file requires them.
func ClientCredentials(certFile, keyFile, caFile string) (credentials.TransportCredentials, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pool, err := loadCertPool(caFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, core.NewError(core.CodeInvalidArgument,
				"worker TLS needs both a client certificate and its key", nil)
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, core.NewError(core.CodeInvalidArgument,
				"worker TLS client certificate or key could not be read", nil)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return credentials.NewTLS(cfg), nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument,
			"worker TLS CA file could not be read", map[string]any{"ca": path})
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, core.NewError(core.CodeInvalidArgument,
			"worker TLS CA file holds no certificate", map[string]any{"ca": path})
	}
	return pool, nil
}

// GRPCServer returns a gRPC server with this service registered. A nil creds
// serves plaintext, which ServeWorkerService admits on loopback only. The
// caller owns the process lifecycle and should stop the server when its
// context ends.
func (s *Service) GRPCServer(creds credentials.TransportCredentials) *grpc.Server {
	opts := []grpc.ServerOption{grpc.MaxConcurrentStreams(maxWorkerStreams)}
	if creds != nil {
		opts = append(opts, grpc.Creds(creds))
	}
	srv := grpc.NewServer(opts...)
	workerv1.RegisterWorkerServiceServer(srv, s)
	return srv
}

// OutboundDials reports how many connections the platform opened to a worker.
// The worker contract has no call that needs one, so a platform that serves a
// worker behind NAT leaves it at zero (REQ-MODEL-089).
func (s *Service) OutboundDials() int64 { return s.dials.Load() }

// ServeWorkerService serves srv on ln until ctx is cancelled or the server
// stops. It returns nil for a clean shutdown. A plaintext listener that is not
// on loopback is refused before it serves: the contract carries pool tokens
// and worker credentials, so off-host traffic needs TLS.
func ServeWorkerService(ctx context.Context, ln net.Listener, srv *grpc.Server, secure bool) error {
	if !secure {
		if err := refuseOffLoopback(ln.Addr()); err != nil {
			_ = ln.Close()
			return err
		}
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		srv.Stop()
		if err := <-done; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return err
		}
		return nil
	case err := <-done:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	}
}

// Join implements WorkerService.
func (s *Service) Join(ctx context.Context, req *workerv1.JoinRequest) (*workerv1.JoinResponse, error) {
	descs, err := descriptorsFromPB(req.GetDescriptors())
	if err != nil {
		return nil, toStatus(err)
	}
	w, err := s.store.Join(ctx, JoinRequest{
		PoolToken:       req.GetPoolToken(),
		ProtocolVersion: req.GetProtocolVersion(),
		Descriptors:     descs,
		Labels:          req.GetLabels(),
		Capacity:        int(req.GetCapacity()),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &workerv1.JoinResponse{
		WorkerId:                 w.ID,
		Credential:               w.Credential,
		HeartbeatIntervalSeconds: int32(w.HeartbeatInterval / time.Second),
	}, nil
}

// Lease implements WorkerService.
func (s *Service) Lease(ctx context.Context, req *workerv1.LeaseRequest) (*workerv1.LeaseResponse, error) {
	leased, err := s.store.Lease(ctx, LeaseRequest{
		WorkerID:   req.GetWorkerId(),
		Credential: req.GetCredential(),
		FreeUnits:  int(req.GetFreeUnits()),
		WarmModels: req.GetWarmModels(),
		TimeLeft:   time.Duration(req.GetTimeLeftSeconds()) * time.Second,
		Wait:       clampLeaseWait(time.Duration(req.GetWaitSeconds()) * time.Second),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	resp := &workerv1.LeaseResponse{}
	for _, inv := range leased {
		pb, err := invocationToPB(inv)
		if err != nil {
			return nil, toStatus(err)
		}
		resp.Invocations = append(resp.Invocations, pb)
	}
	return resp, nil
}

// Starting implements WorkerService.
func (s *Service) Starting(ctx context.Context, req *workerv1.StartingRequest) (*workerv1.StartingResponse, error) {
	err := s.store.Starting(ctx, req.GetWorkerId(), req.GetCredential(), req.GetClientRef(), int(req.GetAttempt()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &workerv1.StartingResponse{}, nil
}

// Started implements WorkerService.
func (s *Service) Started(ctx context.Context, req *workerv1.StartedRequest) (*workerv1.StartedResponse, error) {
	err := s.store.Started(ctx, req.GetWorkerId(), req.GetCredential(), req.GetClientRef(),
		req.GetProviderHandle(), int(req.GetAttempt()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &workerv1.StartedResponse{}, nil
}

// Resume implements WorkerService.
func (s *Service) Resume(ctx context.Context, req *workerv1.ResumeRequest) (*workerv1.ResumeResponse, error) {
	err := s.store.Resume(ctx, req.GetWorkerId(), req.GetCredential(), req.GetClientRef(), int(req.GetAttempt()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &workerv1.ResumeResponse{}, nil
}

// Events implements WorkerService.
func (s *Service) Events(ctx context.Context, req *workerv1.EventsRequest) (*workerv1.EventsResponse, error) {
	events := make([]Event, 0, len(req.GetEvents()))
	for _, e := range req.GetEvents() {
		data, err := payloadBytes(e.GetDataJson())
		if err != nil {
			return nil, toStatus(err)
		}
		events = append(events, Event{
			Seq: e.GetSeq(), Kind: e.GetKind(), Text: e.GetText(), Audio: e.GetAudio(),
			Progress: e.GetProgress(), Attempt: int(e.GetAttempt()), Data: data,
		})
	}
	err := s.store.Events(ctx, req.GetWorkerId(), req.GetCredential(), req.GetClientRef(), int(req.GetAttempt()), events)
	if err != nil {
		return nil, toStatus(err)
	}
	return &workerv1.EventsResponse{}, nil
}

// Finish implements WorkerService.
func (s *Service) Finish(ctx context.Context, req *workerv1.FinishRequest) (*workerv1.FinishResponse, error) {
	result, err := payloadBytes(req.GetResultJson())
	if err != nil {
		return nil, toStatus(err)
	}
	err = s.store.Finish(ctx, req.GetWorkerId(), req.GetCredential(), req.GetClientRef(), req.GetState(),
		result, core.Code(req.GetErrorCode()), req.GetCostMilliPoints(), int(req.GetAttempt()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &workerv1.FinishResponse{}, nil
}

// Release implements WorkerService.
func (s *Service) Release(ctx context.Context, req *workerv1.ReleaseRequest) (*workerv1.ReleaseResponse, error) {
	var notBefore time.Time
	if ms := req.GetNotBeforeUnixMs(); ms > 0 {
		notBefore = time.UnixMilli(ms).UTC()
	}
	err := s.store.Release(ctx, req.GetWorkerId(), req.GetCredential(), req.GetClientRef(),
		req.GetUnused(), notBefore, int(req.GetAttempt()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &workerv1.ReleaseResponse{}, nil
}

// Heartbeat implements WorkerService.
func (s *Service) Heartbeat(ctx context.Context, req *workerv1.HeartbeatRequest) (*workerv1.HeartbeatResponse, error) {
	answer, err := s.store.Heartbeat(ctx, req.GetWorkerId(), req.GetCredential(), int(req.GetFreeUnits()))
	if err != nil {
		return nil, toStatus(err)
	}
	resp := &workerv1.HeartbeatResponse{
		Cancel:         answer.Cancel,
		Drain:          answer.Drain,
		State:          stateToPB(answer.State),
		Credential:     answer.Credential,
		CancelAttempts: map[string]int32{},
	}
	for ref, attempt := range answer.CancelAttempts {
		resp.CancelAttempts[ref] = int32(attempt)
	}
	if !answer.LeaseExpires.IsZero() {
		resp.LeaseExpiresUnixMs = answer.LeaseExpires.UnixMilli()
	}
	return resp, nil
}

// Leave implements WorkerService.
func (s *Service) Leave(ctx context.Context, req *workerv1.LeaveRequest) (*workerv1.LeaveResponse, error) {
	if err := s.store.Leave(ctx, req.GetWorkerId(), req.GetCredential()); err != nil {
		return nil, toStatus(err)
	}
	return &workerv1.LeaveResponse{}, nil
}

// GRPCTransport is the worker's side of basamento.worker.v1 over a connection
// it dialled out itself. It implements Transport, so the worker client and the
// runner are the same code in both profiles (ADR-0012 §4).
type GRPCTransport struct {
	conn   *grpc.ClientConn
	client workerv1.WorkerServiceClient
}

// DialWorkerService dials the platform's WorkerService listener. The worker
// is the side that connects; behind NAT it needs no inbound route. Passing a
// credentials.TransportCredentials (built with ClientCredentials) secures the
// connection; with none the call is plaintext, which the platform admits on
// loopback only.
func DialWorkerService(target string, creds ...credentials.TransportCredentials) (*GRPCTransport, error) {
	var transport credentials.TransportCredentials
	if len(creds) > 0 && creds[0] != nil {
		transport = creds[0]
	} else {
		if err := refusePlaintextTarget(target); err != nil {
			return nil, err
		}
		transport = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, fmt.Errorf("worker: dial %s: %w", target, err)
	}
	return &GRPCTransport{conn: conn, client: workerv1.NewWorkerServiceClient(conn)}, nil
}

// Close releases the connection.
func (t *GRPCTransport) Close() error { return t.conn.Close() }

// Join implements Transport.
func (t *GRPCTransport) Join(ctx context.Context, req JoinRequest) (Worker, error) {
	descs, err := descriptorsToPB(req.Descriptors)
	if err != nil {
		return Worker{}, err
	}
	resp, err := t.client.Join(ctx, &workerv1.JoinRequest{
		PoolToken:       req.PoolToken,
		ProtocolVersion: req.ProtocolVersion,
		Descriptors:     descs,
		Labels:          req.Labels,
		Capacity:        int32(req.Capacity),
	})
	if err != nil {
		return Worker{}, fromStatus(err)
	}
	return Worker{
		ID:                resp.GetWorkerId(),
		Credential:        resp.GetCredential(),
		HeartbeatInterval: time.Duration(resp.GetHeartbeatIntervalSeconds()) * time.Second,
		Descriptors:       req.Descriptors,
		Labels:            req.Labels,
		Capacity:          req.Capacity,
		State:             StateActive,
		Present:           true,
	}, nil
}

// Lease implements Transport.
func (t *GRPCTransport) Lease(ctx context.Context, req LeaseRequest) ([]Invocation, error) {
	resp, err := t.client.Lease(ctx, &workerv1.LeaseRequest{
		WorkerId:        req.WorkerID,
		Credential:      req.Credential,
		FreeUnits:       int32(req.FreeUnits),
		WarmModels:      req.WarmModels,
		TimeLeftSeconds: int32(req.TimeLeft / time.Second),
		WaitSeconds:     int32(req.Wait / time.Second),
	})
	if err != nil {
		return nil, fromStatus(err)
	}
	out := make([]Invocation, 0, len(resp.GetInvocations()))
	for _, pb := range resp.GetInvocations() {
		inv, err := invocationFromPB(pb)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, nil
}

// Starting implements Transport.
func (t *GRPCTransport) Starting(ctx context.Context, workerID, credential, clientRef string, attempt int) error {
	_, err := t.client.Starting(ctx, &workerv1.StartingRequest{
		WorkerId: workerID, Credential: credential, ClientRef: clientRef, Attempt: int32(attempt),
	})
	return fromStatus(err)
}

// Started implements Transport.
func (t *GRPCTransport) Started(ctx context.Context, workerID, credential, clientRef, providerHandle string, attempt int) error {
	_, err := t.client.Started(ctx, &workerv1.StartedRequest{
		WorkerId: workerID, Credential: credential, ClientRef: clientRef,
		ProviderHandle: providerHandle, Attempt: int32(attempt),
	})
	return fromStatus(err)
}

// Resume implements Transport.
func (t *GRPCTransport) Resume(ctx context.Context, workerID, credential, clientRef string, attempt int) error {
	_, err := t.client.Resume(ctx, &workerv1.ResumeRequest{
		WorkerId: workerID, Credential: credential, ClientRef: clientRef, Attempt: int32(attempt),
	})
	return fromStatus(err)
}

// Events implements Transport.
func (t *GRPCTransport) Events(ctx context.Context, workerID, credential, clientRef string, attempt int, events []Event) error {
	batch := make([]*workerv1.Event, 0, len(events))
	for _, e := range events {
		data, err := payloadBytes(e.Data)
		if err != nil {
			return err
		}
		batch = append(batch, &workerv1.Event{
			Seq: e.Seq, Kind: e.Kind, Text: e.Text, Audio: e.Audio,
			Progress: e.Progress, Attempt: int32(e.Attempt), DataJson: data,
		})
	}
	_, err := t.client.Events(ctx, &workerv1.EventsRequest{
		WorkerId: workerID, Credential: credential, ClientRef: clientRef,
		Events: batch, Attempt: int32(attempt),
	})
	return fromStatus(err)
}

// Finish implements Transport.
func (t *GRPCTransport) Finish(ctx context.Context, workerID, credential, clientRef, state string, result json.RawMessage, code core.Code, cost int64, attempt int) error {
	raw, err := payloadBytes(result)
	if err != nil {
		return err
	}
	_, err = t.client.Finish(ctx, &workerv1.FinishRequest{
		WorkerId: workerID, Credential: credential, ClientRef: clientRef, State: state,
		ResultJson: raw, ErrorCode: string(code), CostMilliPoints: cost, Attempt: int32(attempt),
	})
	return fromStatus(err)
}

// Release implements Transport.
func (t *GRPCTransport) Release(ctx context.Context, workerID, credential, clientRef string, unused bool, notBefore time.Time, attempt int) error {
	var ms int64
	if !notBefore.IsZero() {
		ms = notBefore.UnixMilli()
	}
	_, err := t.client.Release(ctx, &workerv1.ReleaseRequest{
		WorkerId: workerID, Credential: credential, ClientRef: clientRef,
		Unused: unused, NotBeforeUnixMs: ms, Attempt: int32(attempt),
	})
	return fromStatus(err)
}

// Heartbeat implements Transport.
func (t *GRPCTransport) Heartbeat(ctx context.Context, workerID, credential string, freeUnits int) (HeartbeatAnswer, error) {
	resp, err := t.client.Heartbeat(ctx, &workerv1.HeartbeatRequest{
		WorkerId: workerID, Credential: credential, FreeUnits: int32(freeUnits),
	})
	if err != nil {
		return HeartbeatAnswer{}, fromStatus(err)
	}
	answer := HeartbeatAnswer{
		Cancel:         resp.GetCancel(),
		Drain:          resp.GetDrain(),
		State:          stateFromPB(resp.GetState()),
		Credential:     resp.GetCredential(),
		CancelAttempts: map[string]int{},
	}
	if ms := resp.GetLeaseExpiresUnixMs(); ms > 0 {
		answer.LeaseExpires = time.UnixMilli(ms).UTC()
	}
	for ref, attempt := range resp.GetCancelAttempts() {
		answer.CancelAttempts[ref] = int(attempt)
	}
	return answer, nil
}

// Leave implements Transport.
func (t *GRPCTransport) Leave(ctx context.Context, workerID, credential string) error {
	_, err := t.client.Leave(ctx, &workerv1.LeaveRequest{WorkerId: workerID, Credential: credential})
	return fromStatus(err)
}

var _ Transport = (*GRPCTransport)(nil)

func descriptorsToPB(descriptors []Descriptor) ([]*workerv1.Descriptor, error) {
	out := make([]*workerv1.Descriptor, 0, len(descriptors))
	for _, d := range descriptors {
		pb, err := descriptorToPB(d)
		if err != nil {
			return nil, err
		}
		out = append(out, pb)
	}
	return out, nil
}

func descriptorsFromPB(descriptors []*workerv1.Descriptor) ([]Descriptor, error) {
	out := make([]Descriptor, 0, len(descriptors))
	for _, pb := range descriptors {
		d, err := descriptorFromPB(pb)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func descriptorToPB(d Descriptor) (*workerv1.Descriptor, error) {
	params, err := rawToStruct(d.Params)
	if err != nil {
		return nil, err
	}
	pb := &workerv1.Descriptor{
		Id: d.ID, Caps: capabilities(d.Caps), ParamsSchema: params, Price: d.Price,
		Protocol: string(d.Protocol), Units: int32(d.Units), Restart: restartToPB(d.RestartSafe),
	}
	for _, in := range d.Inputs {
		pb.Inputs = append(pb.Inputs, &workerv1.InputSlot{
			Name: in.Name, Media: in.Media, Required: in.Required, Max: int32(in.Max),
		})
	}
	return pb, nil
}

func descriptorFromPB(pb *workerv1.Descriptor) (Descriptor, error) {
	params, err := structToRaw(pb.GetParamsSchema())
	if err != nil {
		return Descriptor{}, err
	}
	d := Descriptor{
		Descriptor: model.Descriptor{
			ID:       pb.GetId(),
			Provider: "worker",
			Caps:     capabilitiesFromPB(pb.GetCaps()),
			Params:   params,
			Price:    pb.GetPrice(),
			Protocol: model.Protocol(pb.GetProtocol()),
			Locality: model.LocalityWorker,
		},
		Units:       int(pb.GetUnits()),
		RestartSafe: pb.GetRestart() == workerv1.Restart_RESTART_SAFE,
	}
	for _, in := range pb.GetInputs() {
		d.Inputs = append(d.Inputs, model.InputSlot{
			Name: in.GetName(), Media: in.GetMedia(), Required: in.GetRequired(), Max: int(in.GetMax()),
		})
	}
	return d, nil
}

func invocationToPB(inv Invocation) (*workerv1.Invocation, error) {
	req, err := payloadBytes(inv.Request)
	if err != nil {
		return nil, err
	}
	pb := &workerv1.Invocation{
		ClientRef: inv.ClientRef, Model: inv.Model, RequestJson: req, Units: int32(inv.Units),
		Attempt: int32(inv.Attempt), TraceParent: inv.TraceParent,
	}
	if !inv.LeaseExpires.IsZero() {
		pb.LeaseExpiresUnixMs = inv.LeaseExpires.UnixMilli()
	}
	return pb, nil
}

func invocationFromPB(pb *workerv1.Invocation) (Invocation, error) {
	req, err := payloadJSON(pb.GetRequestJson())
	if err != nil {
		return Invocation{}, err
	}
	inv := Invocation{
		ClientRef: pb.GetClientRef(), Model: pb.GetModel(), Request: req,
		Units: int(pb.GetUnits()), Attempt: int(pb.GetAttempt()), TraceParent: pb.GetTraceParent(),
	}
	if ms := pb.GetLeaseExpiresUnixMs(); ms > 0 {
		inv.LeaseExpires = time.UnixMilli(ms).UTC()
	}
	return inv, nil
}

func capabilities(caps []model.Capability) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, string(c))
	}
	return out
}

func capabilitiesFromPB(caps []string) []model.Capability {
	out := make([]model.Capability, 0, len(caps))
	for _, c := range caps {
		out = append(out, model.Capability(c))
	}
	return out
}

func restartToPB(safe bool) workerv1.Restart {
	if safe {
		return workerv1.Restart_RESTART_SAFE
	}
	return workerv1.Restart_RESTART_UNSPECIFIED
}

func stateToPB(s State) workerv1.WorkerState {
	switch s {
	case StateActive:
		return workerv1.WorkerState_WORKER_STATE_ACTIVE
	case StateDraining:
		return workerv1.WorkerState_WORKER_STATE_DRAINING
	case StatePaused:
		return workerv1.WorkerState_WORKER_STATE_PAUSED
	default:
		return workerv1.WorkerState_WORKER_STATE_UNSPECIFIED
	}
}

func stateFromPB(s workerv1.WorkerState) State {
	switch s {
	case workerv1.WorkerState_WORKER_STATE_ACTIVE:
		return StateActive
	case workerv1.WorkerState_WORKER_STATE_DRAINING:
		return StateDraining
	case workerv1.WorkerState_WORKER_STATE_PAUSED:
		return StatePaused
	default:
		return ""
	}
}

// payloadBytes carries an opaque JSON object to the wire. Raw bytes keep
// every number exactly as the producer wrote it; a Struct would parse it into
// float64 and corrupt integers above 2^53 (REQ-MODEL-090's determinism).
func payloadBytes(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if !isJSONObject(raw) {
		return nil, core.NewError(core.CodeInvalidArgument, "worker: payload is not a JSON object", nil)
	}
	return raw, nil
}

// payloadJSON reads an opaque JSON object from the wire without re-encoding
// it, so its numbers keep the exact text the worker sent.
func payloadJSON(raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if !isJSONObject(raw) {
		return nil, core.NewError(core.CodeInvalidArgument, "worker: payload is not a JSON object", nil)
	}
	return json.RawMessage(append([]byte(nil), raw...)), nil
}

func isJSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

func rawToStruct(raw json.RawMessage) (*structpb.Struct, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "worker: payload is not a JSON object", nil)
	}
	st, err := structpb.NewStruct(m)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "worker: payload cannot be sent", nil)
	}
	return st, nil
}

func structToRaw(st *structpb.Struct) (json.RawMessage, error) {
	if st == nil {
		return nil, nil
	}
	raw, err := json.Marshal(st.AsMap())
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "worker: payload cannot be read", nil)
	}
	return raw, nil
}

// refuseOffLoopback reports whether a plaintext listener is safe: loopback
// only, where the process can prove the peer is on this machine.
func refuseOffLoopback(addr net.Addr) error {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("worker listener %q is not a host:port address", addr), nil)
	}
	if loopbackHost(host) {
		return nil
	}
	return core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("worker listener %q is refused: without TLS it accepts loopback addresses only", addr),
		map[string]any{"key": "worker.listen"})
}

// refusePlaintextTarget keeps a plaintext worker client on loopback, matching
// the platform's rule for its listener. A pool on another machine needs TLS.
func refusePlaintextTarget(target string) error {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("worker: pool URL %q is not a host:port address", target), nil)
	}
	if loopbackHost(host) {
		return nil
	}
	return core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("worker: pool URL %q is refused without TLS: use --pool-tls-cert-file, --pool-tls-key-file and --pool-tls-ca-file", target), nil)
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clampLeaseWait bounds a worker-supplied long poll. Negative values mean no
// wait, as the store treats them.
func clampLeaseWait(wait time.Duration) time.Duration {
	if wait < 0 {
		return 0
	}
	if wait > maxLeaseWait {
		return maxLeaseWait
	}
	return wait
}

func toStatus(err error) error {
	if err == nil {
		return nil
	}
	return status.Error(grpcCode(core.CodeOf(err)), err.Error())
}

func fromStatus(err error) error {
	if err == nil {
		return nil
	}
	st, _ := status.FromError(err)
	return core.NewError(coreCode(st.Code()), st.Message(), nil)
}

func grpcCode(code core.Code) codes.Code {
	switch code {
	case core.CodeInvalidArgument:
		return codes.InvalidArgument
	case core.CodeNotFound:
		return codes.NotFound
	case core.CodeConflict:
		return codes.Aborted
	case core.CodePermissionDenied:
		return codes.PermissionDenied
	case core.CodeUnauthenticated:
		return codes.Unauthenticated
	case core.CodeBudgetExceeded, core.CodeRateLimited:
		return codes.ResourceExhausted
	case core.CodeTimeout:
		return codes.DeadlineExceeded
	case core.CodeUnavailable:
		return codes.Unavailable
	case core.CodeUnsupported:
		return codes.Unimplemented
	case core.CodeCancelled:
		return codes.Canceled
	case CodeLeaseLost:
		return codes.FailedPrecondition
	default:
		return codes.Internal
	}
}

func coreCode(code codes.Code) core.Code {
	switch code {
	case codes.InvalidArgument:
		return core.CodeInvalidArgument
	case codes.NotFound:
		return core.CodeNotFound
	case codes.Aborted:
		return core.CodeConflict
	case codes.PermissionDenied:
		return core.CodePermissionDenied
	case codes.Unauthenticated:
		return core.CodeUnauthenticated
	case codes.ResourceExhausted:
		return core.CodeRateLimited
	case codes.DeadlineExceeded:
		return core.CodeTimeout
	case codes.Unavailable:
		return core.CodeUnavailable
	case codes.Unimplemented:
		return core.CodeUnsupported
	case codes.Canceled:
		return core.CodeCancelled
	case codes.FailedPrecondition:
		return CodeLeaseLost
	default:
		return core.CodeInternal
	}
}
