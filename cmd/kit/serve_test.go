package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/epzero/basamento/core"
)

func testKey() []byte { return bytes.Repeat([]byte{0x42}, 32) }

func TestOpenLocalSecretsAcceptsBase64Key(t *testing.T) {
	raw := "localsecrets://" + base64.StdEncoding.EncodeToString(testKey())
	p, err := openLocalSecrets(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
}

func TestOpenLocalSecretsRejectsMistypedBase64WithoutCreatingAFile(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 31))
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+short)
	if err == nil {
		t.Fatal("openLocalSecrets accepted a 31-byte key")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
	if _, statErr := os.Stat(short); !os.IsNotExist(statErr) {
		t.Fatalf("a mistyped key created a file: %v", statErr)
	}
}

func TestOpenLocalSecretsCreatesMissingKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secrets.key")
	p, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("key file mode = %o, want 600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 32 {
		t.Fatalf("key file holds %d bytes, want 32", len(data))
	}
}

func TestOpenLocalSecretsRejectsLooseKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(path, testKey(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err == nil {
		t.Fatal("openLocalSecrets accepted a group/other-readable key file")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("key file mode changed to %o, want 644 left alone", got)
	}
}

func TestOpenLocalSecretsRejectsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err == nil {
		t.Fatal("openLocalSecrets accepted a directory")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
}

func TestOpenLocalSecretsAcceptsExistingKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(path, testKey(), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
}

func TestOpenLocalSecretsAcceptsFileURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(path, testKey(), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := openLocalSecrets(context.Background(), "localsecrets://file://"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
}
