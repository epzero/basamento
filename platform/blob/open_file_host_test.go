package blob

import (
	"net/url"
	"path/filepath"
	"testing"
)

func TestFileURLHostIsPartOfThePath(t *testing.T) {
	cases := []struct {
		raw  string
		join string
	}{
		{"file://./data/blobs", "data/blobs"},
		{"file://data/blobs", "data/blobs"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := fileDir(u)
			if err != nil {
				t.Fatal(err)
			}
			want, err := filepath.Abs(filepath.FromSlash(tc.join))
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("fileDir(%q) = %q, want %q", tc.raw, got, want)
			}
		})
	}
}

func TestFileURLLocalhostHostIsDropped(t *testing.T) {
	u, err := url.Parse("file://localhost/tmp/blobs")
	if err != nil {
		t.Fatal(err)
	}
	got, err := fileDir(u)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/blobs" {
		t.Fatalf("fileDir = %q, want /tmp/blobs", got)
	}
}
