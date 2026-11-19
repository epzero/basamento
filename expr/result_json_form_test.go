package expr

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestMapLiteralInfNoJSONForm(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{'x': 1.0/0.0}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Eval(nil)
	if err == nil || !strings.Contains(err.Error(), "no JSON form") {
		t.Fatalf("got %v", err)
	}
}

func TestListLiteralInfNoJSONForm(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`[1.0/0.0]`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Eval(nil)
	if err == nil || !strings.Contains(err.Error(), "no JSON form") {
		t.Fatalf("got %v", err)
	}
}

func TestDurationMapJSONForm(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{'d': duration('1s')}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.(map[string]any)
	if !ok || m["d"] != "1s" {
		t.Fatalf("got %v", res)
	}
}

func TestTimestampMapJSONForm(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{'t': timestamp('2020-01-01T00:00:00Z')}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.(map[string]any)
	if !ok || m["t"] == nil {
		t.Fatalf("got %v", res)
	}
}

func TestUint64MaxJSONNumber(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{'u': 18446744073709551615u}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("got %T", res)
	}
	if m["u"] == nil {
		t.Fatalf("got %v", res)
	}
}

func TestBytesMapJSONForm(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{'b': b'hi'}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("got %T", res)
	}
	want := base64.StdEncoding.EncodeToString([]byte("hi"))
	if m["b"] != want {
		t.Fatalf("got %v want %s", m["b"], want)
	}
}

func TestNaNMapLiteralNoJSONForm(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{'n': 0.0/0.0}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Eval(nil)
	if err == nil || !strings.Contains(err.Error(), "no JSON form") {
		t.Fatalf("got %v", err)
	}
}
