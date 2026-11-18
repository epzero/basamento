package expr

import (
	"encoding/json"
	"testing"
)

func TestTimestampGetHoursRejectsLocalTZ(t *testing.T) {
	env, err := NewEnv([]Var{{Name: "at", Schema: json.RawMessage(`{"type":"string","format":"date-time"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`at.getHours('Local')`, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", "Asia/Tokyo")
	_, err = p.Eval(map[string]any{"at": "2026-11-16T18:30:00Z"})
	if err == nil {
		t.Fatal("want error for Local timezone")
	}
	t.Setenv("TZ", "Europe/Rome")
	_, err = p.Eval(map[string]any{"at": "2026-11-16T18:30:00Z"})
	if err == nil {
		t.Fatal("want error independent of host TZ")
	}
}