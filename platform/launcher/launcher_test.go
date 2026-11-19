package launcher_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/launcher"
	"github.com/epzero/basamento/platform/launcher/conformance"
)

func TestHTTPConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) platform.Launcher {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(srv.Close)
		l, err := launcher.NewHTTP(launcher.HTTPConfig{URL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		return l
	})
}

func TestSimConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) platform.Launcher { return launcher.NewSim() })
}

func TestProcessConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) platform.Launcher {
		l, err := launcher.NewProcess(launcher.ProcessConfig{Command: []string{"sh", "-c", "exit 0"}})
		if err != nil {
			t.Fatal(err)
		}
		return l
	})
}

type staticCredential string

func (s staticCredential) Resolve(context.Context, string) ([]byte, error) { return []byte(s), nil }

func TestHTTPSendsTheTemplatedRequest(t *testing.T) {
	var got struct {
		Pool     string            `json:"pool"`
		Template string            `json:"template"`
		Count    int               `json:"count"`
		Tokens   []string          `json:"join_tokens"`
		Key      string            `json:"idempotency_key"`
		Settings map[string]string `json:"settings"`
		Auth     string
		Path     string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Auth = r.Header.Get("Authorization")
		got.Path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	l, err := launcher.NewHTTP(launcher.HTTPConfig{
		URL:         srv.URL + "/pool/{pool}/template/{template}?count={count}&key={idempotency_key}",
		Credential:  "cred://cloud/launcher",
		Credentials: staticCredential("secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := l.Launch(context.Background(), platform.LaunchRequest{
		Pool: "acme-gpu", Template: "sdxl-gpu", Count: 2,
		JoinTokens: []string{"t1", "t2"}, IdempotencyKey: "launch-1",
		Settings: map[string]string{"region": "eu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatal("not accepted")
	}
	if got.Path != "/pool/acme-gpu/template/sdxl-gpu" {
		t.Errorf("path %q", got.Path)
	}
	if got.Auth != "Bearer secret" {
		t.Errorf("authorization %q", got.Auth)
	}
	if got.Pool != "acme-gpu" || got.Template != "sdxl-gpu" || got.Count != 2 || got.Key != "launch-1" {
		t.Errorf("request %+v", got)
	}
	if len(got.Tokens) != 2 || got.Tokens[0] != "t1" || got.Tokens[1] != "t2" {
		t.Errorf("tokens %v", got.Tokens)
	}
	if got.Settings["region"] != "eu" {
		t.Errorf("settings %v", got.Settings)
	}
}

func TestHTTPReportsANon2xxAsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	l, err := launcher.NewHTTP(launcher.HTTPConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.Launch(context.Background(), platform.LaunchRequest{
		Pool: "p", Template: "t", Count: 1, JoinTokens: []string{"t1"}, IdempotencyKey: "k",
	})
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("got %v, want unavailable", err)
	}
}

func TestHTTPRefusesCredentialWithoutResolver(t *testing.T) {
	_, err := launcher.NewHTTP(launcher.HTTPConfig{URL: "http://example.invalid", Credential: "cred://a"})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v, want invalid_argument", err)
	}
}

func TestSimRecordsAndReports(t *testing.T) {
	sim := launcher.NewSim()
	req := platform.LaunchRequest{Pool: "p", Template: "t", Count: 2, JoinTokens: []string{"t1", "t2"}, IdempotencyKey: "k"}
	if _, err := sim.Launch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if sim.Asked("t") != 2 {
		t.Fatalf("asked %d", sim.Asked("t"))
	}
	sim.SetAccept(false)
	res, err := sim.Launch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted {
		t.Fatal("accepted after refusing")
	}
	sim.SetFail(errors.New("no quota"))
	if _, err := sim.Launch(context.Background(), req); err == nil {
		t.Fatal("launch succeeded with Fail set")
	}
	if sim.LastLaunchError(context.Background()) != "no quota" {
		t.Fatalf("last error %q", sim.LastLaunchError(context.Background()))
	}
}
