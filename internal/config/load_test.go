package config

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kit.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPrecedence(t *testing.T) {
	path := writeConfig(t, "listen:\n  addr: 127.0.0.1:9000\nlog:\n  level: debug\n")
	got, err := Load(Options{
		File: path,
		Env:  []string{"KIT_LISTEN_ADDR=127.0.0.1:7000"},
		Flags: map[string]string{
			"listen.addr": "127.0.0.1:6000",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v, src, _ := got.Setting("listen.addr"); v != "127.0.0.1:6000" || src != SourceFlag {
		t.Fatalf("listen.addr = %q from %q, want flag", v, src)
	}
	if v, src, _ := got.Setting("log.level"); v != "debug" || src != SourceFile {
		t.Fatalf("log.level = %q from %q, want file", v, src)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, "log:\n  level: debug\n")
	got, err := Load(Options{File: path, Env: []string{"KIT_LOG_LEVEL=error"}})
	if err != nil {
		t.Fatal(err)
	}
	if v, src, _ := got.Setting("log.level"); v != "error" || src != SourceEnv {
		t.Fatalf("log.level = %q from %q, want env", v, src)
	}
}

func TestLoadDefaultsAndEnvName(t *testing.T) {
	got, err := Load(Options{})
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs("./.kit/data")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"profile":          ProfileEmbedded,
		"listen.addr":      "127.0.0.1:8080",
		"data.dir":         "./.kit/data",
		"log.level":        "info",
		"log.format":       "text",
		"sql.url":          "sqlite://" + abs + "/kit.db",
		"blob.url":         "file://" + abs + "/blobs",
		"bus.url":          "nats-embedded://" + abs + "/nats",
		"secrets.url":      "localsecrets://" + abs + "/secrets.key",
		"shutdown.timeout": "10s",
	}
	for key, want := range cases {
		if v, src, _ := got.Setting(key); v != want || src != SourceDefault {
			t.Errorf("%s = %q from %q, want %q from default", key, v, src, want)
		}
	}
	if EnvironName("listen.addr") != "KIT_LISTEN_ADDR" {
		t.Fatalf("env name = %q", EnvironName("listen.addr"))
	}
}

func TestLoadExpandsDataDir(t *testing.T) {
	got, err := Load(Options{
		Env:   []string{"KIT_DATA_DIR=/var/lib/kit"},
		Flags: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _ := got.Setting("sql.url"); v != "sqlite:///var/lib/kit/kit.db" {
		t.Fatalf("sql.url = %q", v)
	}
	if v, _, _ := got.Setting("blob.url"); v != "file:///var/lib/kit/blobs" {
		t.Fatalf("blob.url = %q", v)
	}
	if v, _, _ := got.Setting("bus.url"); v != "nats-embedded:///var/lib/kit/nats" {
		t.Fatalf("bus.url = %q", v)
	}
	if v, _, _ := got.Setting("secrets.url"); v != "localsecrets:///var/lib/kit/secrets.key" {
		t.Fatalf("secrets.url = %q", v)
	}
}

func TestLoadCloudHasNoPortDefaults(t *testing.T) {
	got, err := Load(Options{Env: []string{"KIT_PROFILE=cloud"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sql.url", "blob.url", "bus.url", "secrets.url"} {
		if v, _, _ := got.Setting(key); v != "" {
			t.Fatalf("%s = %q, want no default in cloud", key, v)
		}
	}
}

func TestLoadDoesNotExpandDataDirInExplicitURLs(t *testing.T) {
	got, err := Load(Options{Env: []string{
		"KIT_DATA_DIR=/srv/kit",
		"KIT_SQL_URL=sqlite:///tmp/$(data.dir)/kit.db",
	}})
	if err != nil {
		t.Fatal(err)
	}
	const want = "sqlite:///tmp/$(data.dir)/kit.db"
	if v, _, _ := got.Setting("sql.url"); v != want {
		t.Fatalf("sql.url = %q, want %q", v, want)
	}
}

func TestLoadEscapesDataDirInDefaultURLs(t *testing.T) {
	got, err := Load(Options{Env: []string{"KIT_DATA_DIR=/tmp/a#b"}})
	if err != nil {
		t.Fatal(err)
	}
	for key, wantPath := range map[string]string{
		"sql.url":     "/tmp/a#b/kit.db",
		"blob.url":    "/tmp/a#b/blobs",
		"bus.url":     "/tmp/a#b/nats",
		"secrets.url": "/tmp/a#b/secrets.key",
	} {
		v, _, _ := got.Setting(key)
		u, err := url.Parse(v)
		if err != nil {
			t.Fatalf("%s = %q: %v", key, v, err)
		}
		if u.Path != wantPath || u.Fragment != "" {
			t.Fatalf("%s = %q parses to path %q fragment %q, want path %q with no fragment", key, v, u.Path, u.Fragment, wantPath)
		}
	}
}
