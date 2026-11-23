package worker

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
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

func TestCodesSurviveTheStatusRoundTrip(t *testing.T) {
	for _, code := range []core.Code{
		core.CodeInvalidArgument, core.CodeNotFound, core.CodeConflict, core.CodeUnauthenticated,
		core.CodeUnavailable, CodeLeaseLost,
	} {
		err := core.NewError(code, "refused", nil)
		if got := core.CodeOf(fromStatus(toStatus(err))); got != code {
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
