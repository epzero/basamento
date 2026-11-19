package expr

import (
	"encoding/json"
	"testing"
)

func TestTimestampHours(t *testing.T) {
	env, err := NewEnv([]Var{{Name: "at", Schema: json.RawMessage(`{"type":"string","format":"date-time"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`at.getHours('Europe/Rome')`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(map[string]any{"at": "2026-11-16T18:30:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if res != int64(19) {
		t.Fatalf("got %v want 19", res)
	}
}
