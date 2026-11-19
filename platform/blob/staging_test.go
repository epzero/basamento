package blob

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core/coretest"
)

func TestStagingKeyGrammar(t *testing.T) {
	t.Parallel()
	ok := "stage_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLM1234"
	if !ValidateStagingKey(ok) {
		t.Fatalf("valid key rejected")
	}
	for _, bad := range []string{"", "stage_short", "STAGE_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLM1234", "stage_../../x", "sha256:" + strings.Repeat("a", 64)} {
		if ValidateStagingKey(bad) {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestNewStagingKeysDistinct(t *testing.T) {
	t.Parallel()
	r := coretest.SeededRand(3)
	a, err := newStagingKey(r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newStagingKey(r)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("duplicate staging keys")
	}
}

func TestUploadContentTypeBounds(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"text/plain", "application/octet-stream"} {
		if !validBoundContentType(ok) {
			t.Fatalf("reject %q", ok)
		}
	}
	for _, bad := range []string{"", "text", "text/plain, text/markdown", "text/*", "text/plain; charset=utf-8"} {
		if validBoundContentType(bad) {
			t.Fatalf("accept %q", bad)
		}
	}
}
