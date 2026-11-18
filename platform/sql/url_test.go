package sql

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestParseStoreURLRejectsUnsupported(t *testing.T) {
	cases := []string{
		"mysql://kit@db.example.test/kit",
		"file:///{data}/kit.db",
		"sqlite://",
		"postgres://kit@db.example.test",
		"kit.db",
		"postgresql://u:p@h/db",
		"sqlite:///x?query=1",
		"sqlite://x?_pragma=journal_mode(WAL)",
	}
	for _, raw := range cases {
		if _, err := parseStoreURL(raw); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("parseStoreURL(%q) = %v, want invalid_argument", raw, err)
		}
	}
}

func TestParseStoreURLSQLiteRelativeHostPath(t *testing.T) {
	p, err := parseStoreURL("sqlite://data/kit.db")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs("data/kit.db")
	if p.sqlitePath != want {
		t.Fatalf("got %q want %q", p.sqlitePath, want)
	}
}

func TestParseStoreURLSQLiteAbsolutePath(t *testing.T) {
	p, err := parseStoreURL("sqlite:///tmp/abs/kit.db")
	if err != nil {
		t.Fatal(err)
	}
	if p.sqlitePath != "/tmp/abs/kit.db" {
		t.Fatalf("got %q", p.sqlitePath)
	}
}

func TestParseStoreURLSQLiteMemory(t *testing.T) {
	for _, raw := range []string{"sqlite://:memory:", "sqlite:///:memory:"} {
		p, err := parseStoreURL(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if !sqliteMemoryPath(p.sqlitePath) {
			t.Fatalf("%q path %q", raw, p.sqlitePath)
		}
		if !strings.HasPrefix(sqliteDSN(p.sqlitePath), "file::memory:") {
			t.Fatalf("dsn %q", sqliteDSN(p.sqlitePath))
		}
	}
}

func TestSQLiteDSNEscapesPath(t *testing.T) {
	dsn := sqliteDSN("/tmp/has space/db.db")
	if strings.Contains(dsn, "has space") {
		t.Fatalf("unescaped space in %q", dsn)
	}
	if !strings.Contains(dsn, "busy_timeout") || !strings.Contains(dsn, "_txlock=immediate") {
		t.Fatalf("pragma order: %q", dsn)
	}
	idxBusy := strings.Index(dsn, "busy_timeout")
	idxWal := strings.Index(dsn, "journal_mode")
	if idxBusy > idxWal {
		t.Fatalf("busy_timeout must precede journal_mode in %q", dsn)
	}
}

func TestRedactURLStripsPassword(t *testing.T) {
	raw := "postgres://kit:s3cret-pw@127.0.0.1:9/kit"
	got := redactURL(raw)
	if stringsContains(got, "s3cret-pw") {
		t.Fatalf("redactURL leaked password: %s", got)
	}
}

func TestRedactURLStripsQuerySecrets(t *testing.T) {
	raw := "postgres://u@h/db?sslpassword=sslpw&password=qpw"
	got := redactURL(raw)
	if stringsContains(got, "sslpw") || stringsContains(got, "qpw") {
		t.Fatalf("leaked query secret: %s", got)
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
