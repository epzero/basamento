package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func loadValidate(t *testing.T, opts Options) []error {
	t.Helper()
	got, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	return got.Validate()
}

func errorKey(t *testing.T, err error) any {
	t.Helper()
	var e *core.Error
	if !errors.As(err, &e) {
		t.Fatalf("%v is not a *core.Error", err)
	}
	return e.Details["key"]
}

func assertKeys(t *testing.T, errs []error, want ...string) {
	t.Helper()
	if len(errs) != len(want) {
		t.Fatalf("got %d errors (%v), want %d naming %v", len(errs), errs, len(want), want)
	}
	for i, key := range want {
		if got := errorKey(t, errs[i]); got != key {
			t.Fatalf("error %d details = %v, want key %q", i, got, key)
		}
		if !strings.Contains(errs[i].Error(), key) {
			t.Fatalf("error %d = %q does not name %q", i, errs[i], key)
		}
	}
}

func TestValidateDefaultsAreValid(t *testing.T) {
	if errs := loadValidate(t, Options{}); len(errs) != 0 {
		t.Fatalf("defaults: %v", errs)
	}
}

func TestValidateReportsEveryErrorOnce(t *testing.T) {
	got, err := Load(Options{
		File: writeConfig(t, "profile: desktop\nlisten:\n  addr: \"8080\"\n"),
		Env:  []string{"KIT_SQL_URL=://bad-url"},
	})
	if err != nil {
		t.Fatal(err)
	}
	errs := got.Validate()
	assertKeys(t, errs, "profile", "listen.addr", "sql.url")
	for _, err := range errs {
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("code = %s", core.CodeOf(err))
		}
	}
}

func TestValidateProfiles(t *testing.T) {
	for _, profile := range []string{"desktop", "Embedded", ""} {
		errs := loadValidate(t, Options{File: writeConfig(t, "profile: \""+profile+"\"\n")})
		assertKeys(t, errs, "profile")
		if strings.Contains(errs[0].Error(), profile) && profile != "" {
			t.Fatalf("error echoes profile %q: %v", profile, errs[0])
		}
	}
}

func TestValidateCloudNeedsEveryPortURL(t *testing.T) {
	valid := map[string]string{
		"sql.url":     "postgres://db.example.test/kit",
		"blob.url":    "s3://kit-buckets/assets",
		"bus.url":     "nats://bus.example.test:4222",
		"secrets.url": "awskms:///arn:aws:kms:kit",
	}
	for omit := range valid {
		opts := Options{Env: []string{"KIT_PROFILE=cloud"}}
		for key, value := range valid {
			if key != omit {
				opts.Env = append(opts.Env, EnvironName(key)+"="+value)
			}
		}
		errs := loadValidate(t, opts)
		assertKeys(t, errs, omit)
	}
}

func TestValidateSchemePerProfile(t *testing.T) {
	cases := []struct {
		profile string
		key     string
		url     string
		impl    string
		wantErr bool
	}{
		{ProfileEmbedded, "sql.url", "sqlite:///data/kit.db", "sqlite", false},
		{ProfileEmbedded, "sql.url", "postgres://db.example.test/kit", "", true},
		{ProfileEmbedded, "blob.url", "file:///data/blobs", "file", false},
		{ProfileEmbedded, "blob.url", "bbolt:///var/lib/kit/blobs", "", true},
		{ProfileEmbedded, "bus.url", "nats-embedded:///data/nats", "nats-embedded", false},
		{ProfileEmbedded, "bus.url", "amqp://bus.example.test", "", true},
		{ProfileEmbedded, "secrets.url", "localsecrets:///data/secrets.key", "localsecrets", false},
		{ProfileEmbedded, "secrets.url", "vault://vault.example.test", "", true},
		{ProfileCloud, "sql.url", "postgres://db.example.test/kit", "postgres", false},
		{ProfileCloud, "blob.url", "gs://kit-buckets/assets", "gcs", false},
		{ProfileCloud, "blob.url", "azblob://assets?account=kit", "azblob", false},
		{ProfileCloud, "bus.url", "nats://bus.example.test:4222", "nats", false},
		{ProfileCloud, "secrets.url", "hashivault://vault.example.test", "hashivault", false},
		{ProfileCloud, "sql.url", "sqlite:///data/kit.db", "", true},
	}
	base := map[string]map[string]string{
		ProfileEmbedded: {
			"sql.url":     "sqlite:///data/kit.db",
			"blob.url":    "file:///data/blobs",
			"bus.url":     "nats-embedded:///data/nats",
			"secrets.url": "localsecrets:///data/secrets.key",
		},
		ProfileCloud: {
			"sql.url":     "postgres://db.example.test/kit",
			"blob.url":    "s3://kit-buckets/assets",
			"bus.url":     "nats://bus.example.test:4222",
			"secrets.url": "awskms:///arn:aws:kms:kit",
		},
	}
	for _, tc := range cases {
		urls := map[string]string{}
		for key, value := range base[tc.profile] {
			urls[key] = value
		}
		urls[tc.key] = tc.url
		opts := Options{Env: []string{"KIT_PROFILE=" + tc.profile}}
		for key, value := range urls {
			opts.Env = append(opts.Env, EnvironName(key)+"="+value)
		}
		got, err := Load(opts)
		if err != nil {
			t.Fatal(err)
		}
		errs := got.Validate()
		if tc.wantErr {
			if len(errs) != 1 {
				t.Fatalf("%s %s: errors = %v", tc.profile, tc.url, errs)
			}
			if got := errorKey(t, errs[0]); got != tc.key {
				t.Fatalf("details = %v", got)
			}
			continue
		}
		if len(errs) != 0 {
			t.Fatalf("%s %s: errors = %v", tc.profile, tc.url, errs)
		}
		if impl := got.Config.Implementation(strings.TrimSuffix(tc.key, ".url")); impl != tc.impl {
			t.Fatalf("%s implementation = %q, want %q", tc.key, impl, tc.impl)
		}
	}
}

func TestValidateErrorRedaction(t *testing.T) {
	secretURL := "nats://ana:hunter2@nats.example.test:4222/bus?token=s3cr3t"
	errs := loadValidate(t, Options{File: writeConfig(t, "bus:\n  url: \""+secretURL+"\"\n")})
	assertKeys(t, errs, "bus.url")
	msg := errs[0].Error()
	for _, leaked := range []string{"ana", "hunter2", "nats.example.test", "4222", "/bus", "token", "s3cr3t"} {
		if strings.Contains(msg, leaked) {
			t.Fatalf("error leaks %q: %s", leaked, msg)
		}
	}
	if !strings.Contains(msg, `"nats"`) {
		t.Fatalf("error does not name the scheme: %s", msg)
	}
}

func TestValidateUnparseableAndNonURLAreNotEchoed(t *testing.T) {
	cases := map[string]string{
		"sql.url": "postgres://ana:hunter2@[bad",
		"profile": "secret-profile",
	}
	for key, value := range cases {
		errs := loadValidate(t, Options{File: writeConfig(t, key+": \""+value+"\"\n")})
		if len(errs) != 1 {
			t.Fatalf("%s: %v", key, errs)
		}
		if got := errorKey(t, errs[0]); got != key {
			t.Fatalf("%s details = %v", key, got)
		}
		for _, leaked := range []string{"ana", "hunter2", "[bad", "secret-profile", "://"} {
			if strings.Contains(errs[0].Error(), leaked) {
				t.Fatalf("%s error leaks %q: %s", key, leaked, errs[0])
			}
		}
	}
}
