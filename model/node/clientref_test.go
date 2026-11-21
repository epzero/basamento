package node

import (
	"strings"
	"testing"
)

func TestClientRefIsStableForOneInvocation(t *testing.T) {
	t.Parallel()
	input := []byte(`{"params":{"duration_s":5}}`)
	first := ClientRef([]byte("run-a"), "model-node_1", "generate", input)
	again := ClientRef([]byte("run-a"), "model-node_1", "generate", input)
	if first != again {
		t.Fatalf("refs differ: %q and %q", first, again)
	}
	if !strings.HasPrefix(first, clientRefPrefix) {
		t.Fatalf("ref %q has no node prefix", first)
	}
	if len(first) != len(clientRefPrefix)+64 {
		t.Fatalf("ref %q is not a sha256 hex digest", first)
	}
}

func TestClientRefIgnoresJSONFormatting(t *testing.T) {
	t.Parallel()
	compact := ClientRef([]byte("run-a"), "v1", "generate", []byte(`{"params":{"duration_s":5,"tier":"a"}}`))
	spaced := ClientRef([]byte("run-a"), "v1", "generate", []byte(`{ "params": { "duration_s": 5, "tier": "a" } }`))
	if compact != spaced {
		t.Fatalf("canonical input should hash alike: %q != %q", compact, spaced)
	}
}

func TestClientRefSeparatesVisitsNodesAndInputs(t *testing.T) {
	t.Parallel()
	input := []byte(`{"params":{"duration_s":5}}`)
	base := ClientRef([]byte("run-a"), "model-node_1", "generate", input)
	cases := map[string]string{
		"another visit": ClientRef([]byte("run-a"), "model-node_2", "generate", input),
		"another node":  ClientRef([]byte("run-a"), "model-node_1", "render", input),
		"another input": ClientRef([]byte("run-a"), "model-node_1", "generate", []byte(`{"params":{"duration_s":6}}`)),
	}
	for name, ref := range cases {
		if ref == base {
			t.Fatalf("%s produced the same ref", name)
		}
	}
}

func TestClientRefCarriesNoInputText(t *testing.T) {
	t.Parallel()
	input := []byte(`{"params":{"prompt":"a secret","duration_s":5}}`)
	ref := ClientRef([]byte("run-a"), "v1", "generate", input)
	if strings.Contains(ref, "secret") || strings.Contains(ref, "duration_s") {
		t.Fatalf("ref %q echoes the input", ref)
	}
}

func TestClientRefSeparatesRuns(t *testing.T) {
	t.Parallel()
	input := []byte(`{"params":{"duration_s":5}}`)
	if ClientRef([]byte("run-a"), "model-node_1", "generate", input) == ClientRef([]byte("run-b"), "model-node_1", "generate", input) {
		t.Fatal("the same visit in two runs produced the same ref")
	}
}

func TestClientRefReplayKeepsTheSalt(t *testing.T) {
	t.Parallel()
	input := []byte(`{"params":{"duration_s":5}}`)
	salt := []byte("run-a-salt")
	if ClientRef(salt, "model-node_1", "generate", input) != ClientRef(append([]byte(nil), salt...), "model-node_1", "generate", input) {
		t.Fatal("the same run salt produced different refs")
	}
}

func TestClientRefSeparatesFieldBoundaries(t *testing.T) {
	t.Parallel()
	a := ClientRef(nil, "ab", "c", []byte(`{}`))
	b := ClientRef(nil, "a", "bc", []byte(`{}`))
	if a == b {
		t.Fatal("visit and node boundary is ambiguous")
	}
}
