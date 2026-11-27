package worker

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	workerv1 "github.com/epzero/basamento/model/worker/pb"
	"google.golang.org/protobuf/proto"
)

func TestDescriptorRoundTripsOverTheWire(t *testing.T) {
	in := descriptorForWireTest()
	pb, err := descriptorToPB(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := descriptorFromPB(pb)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.Units != in.Units || out.RestartSafe != in.RestartSafe {
		t.Fatalf("descriptor round trip = %+v, want %+v", out, in)
	}
	if string(out.Params) != `{"type":"object"}` {
		t.Fatalf("params = %s, want the schema back", out.Params)
	}
	if len(out.Caps) != 1 || out.Caps[0] != model.CapChat {
		t.Fatalf("caps = %v, want [chat]", out.Caps)
	}
	if out.Locality != model.LocalityWorker {
		t.Fatalf("locality = %q, want worker", out.Locality)
	}
}

func TestInvocationRoundTripsOverTheWire(t *testing.T) {
	in := Invocation{
		ClientRef: "v1", Model: "sdxl", Units: 1, Attempt: 2,
		Request: json.RawMessage(`{"model":"sdxl","client_ref":"v1"}`),
	}
	pb, err := invocationToPB(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := invocationFromPB(pb)
	if err != nil {
		t.Fatal(err)
	}
	if out.ClientRef != in.ClientRef || out.Model != in.Model || out.Attempt != in.Attempt {
		t.Fatalf("invocation round trip = %+v, want %+v", out, in)
	}
	var req model.Request
	if err := json.Unmarshal(out.Request, &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "sdxl" || req.ClientRef != "v1" {
		t.Fatalf("request = %+v, want the model request back", req)
	}
}

func TestCodesSurviveTheConnectRoundTrip(t *testing.T) {
	for _, code := range []core.Code{
		core.CodeInvalidArgument, core.CodeNotFound, core.CodeConflict, core.CodeUnauthenticated,
		core.CodeUnavailable, CodeLeaseLost,
	} {
		err := core.NewError(code, "refused", nil)
		if got := core.CodeOf(fromConnect(toConnect(err))); got != code {
			t.Fatalf("code %q came back as %q", code, got)
		}
	}
}

func descriptorForWireTest() Descriptor {
	return Descriptor{
		Descriptor: model.Descriptor{
			ID: "sdxl", Provider: "worker", Caps: []model.Capability{model.CapChat},
			Params: json.RawMessage(`{"type":"object"}`), Protocol: model.ProtocolSingle,
			Locality: model.LocalityWorker,
		},
		Units: 1,
	}
}

// TestOpaquePayloadsKeepLargeIntegers guards the bytes fields against a
// regression to google.protobuf.Struct, which parsed JSON into float64 and
// silently turned a seed of 2^53+1 into 2^53.
func TestOpaquePayloadsKeepLargeIntegers(t *testing.T) {
	const payload = `{"seed":9007199254740993}`
	inv, err := invocationToPB(Invocation{ClientRef: "v1", Model: "sdxl", Request: json.RawMessage(payload)})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := proto.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	var back workerv1.Invocation
	if err := proto.Unmarshal(wire, &back); err != nil {
		t.Fatal(err)
	}
	out, err := invocationFromPB(&back)
	if err != nil {
		t.Fatal(err)
	}
	var req model.Request
	if err := json.Unmarshal(out.Request, &req); err != nil {
		t.Fatal(err)
	}
	if req.Seed == nil || *req.Seed != 9007199254740993 {
		t.Fatalf("seed = %v, want 9007199254740993", req.Seed)
	}

	data, err := payloadBytes(json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	eventWire, err := proto.Marshal(&workerv1.Event{DataJson: data})
	if err != nil {
		t.Fatal(err)
	}
	var event workerv1.Event
	if err := proto.Unmarshal(eventWire, &event); err != nil {
		t.Fatal(err)
	}
	if got, err := payloadBytes(event.GetDataJson()); err != nil || string(got) != payload {
		t.Fatalf("event data = %s, err %v, want %s", got, err, payload)
	}

	result, err := payloadBytes(json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	finishWire, err := proto.Marshal(&workerv1.FinishRequest{ResultJson: result})
	if err != nil {
		t.Fatal(err)
	}
	var finish workerv1.FinishRequest
	if err := proto.Unmarshal(finishWire, &finish); err != nil {
		t.Fatal(err)
	}
	if got, err := payloadBytes(finish.GetResultJson()); err != nil || string(got) != payload {
		t.Fatalf("result = %s, err %v, want %s", got, err, payload)
	}
}
