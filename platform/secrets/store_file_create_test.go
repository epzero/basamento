package secrets

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateIfAbsentNeverLeavesTruncatedRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keysets")
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := StoredKeyset{Wrapped: []byte("full-wrap"), MasterKeyID: "mk", Revision: 1}
	ok, err := s.CreateIfAbsent(context.Background(), "acme", rec)
	if err != nil || !ok {
		t.Fatalf("create ok=%v err=%v", ok, err)
	}
	p, err := s.path("acme")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 20 {
		t.Fatalf("truncated record %q", b)
	}
	got, found, err := s.Get(context.Background(), "acme")
	if err != nil || !found || string(got.Wrapped) != "full-wrap" {
		t.Fatalf("get %+v found=%v err=%v", got, found, err)
	}
}

func TestCreateIfAbsentLeavesCorruptFileUnchanged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keysets")
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.path("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := s.Get(context.Background(), "acme")
	if found || err == nil {
		t.Fatalf("corrupt record: found=%v err=%v", found, err)
	}
	ok, err := s.CreateIfAbsent(context.Background(), "acme", StoredKeyset{Wrapped: []byte("w"), MasterKeyID: "mk", Revision: 1})
	if err != nil || ok {
		t.Fatalf("create on corrupt: ok=%v err=%v", ok, err)
	}
	_, found, err = s.Get(context.Background(), "acme")
	if found || err == nil {
		t.Fatalf("still corrupt: found=%v err=%v", found, err)
	}
}
