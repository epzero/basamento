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

func TestSQLiteMemoryPathNotFileSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit:memory:")
	if sqliteMemoryPath(path) {
		t.Fatalf("%q must not be memory db", path)
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

func TestRedactURLReplacesSecretsLiterally(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "userinfo password",
			raw:  "postgres://kit:s3cret-pw@127.0.0.1:9/kit?connect_timeout=1",
			want: "postgres://kit:***@127.0.0.1:9/kit?connect_timeout=1",
		},
		{
			name: "password query",
			raw:  "postgres://127.0.0.1:9/kit?password=s3cret-pw",
			want: "postgres://127.0.0.1:9/kit?password=***",
		},
		{
			name: "sslpassword query",
			raw:  "postgres://127.0.0.1:9/kit?sslpassword=ssl-secret",
			want: "postgres://127.0.0.1:9/kit?sslpassword=***",
		},
		{
			name: "passfile query keeps the path unescaped",
			raw:  "postgres://127.0.0.1:9/kit?passfile=/tmp/pgpass",
			want: "postgres://127.0.0.1:9/kit?passfile=***",
		},
		{
			name: "sslcert query",
			raw:  "postgres://127.0.0.1:9/kit?sslcert=/tmp/client.crt",
			want: "postgres://127.0.0.1:9/kit?sslcert=***",
		},
		{
			name: "sslkey query",
			raw:  "postgres://127.0.0.1:9/kit?sslkey=/tmp/client.key",
			want: "postgres://127.0.0.1:9/kit?sslkey=***",
		},
		{
			name: "sslrootcert query",
			raw:  "postgres://127.0.0.1:9/kit?sslrootcert=/tmp/root.crt",
			want: "postgres://127.0.0.1:9/kit?sslrootcert=***",
		},
		{
			name: "other parameters keep their order and value",
			raw:  "postgres://u@h/db?sslmode=verify-full&sslrootcert=/tmp/root.crt&connect_timeout=1",
			want: "postgres://u@h/db?sslmode=verify-full&sslrootcert=***&connect_timeout=1",
		},
		{
			name: "userinfo and query together",
			raw:  "postgres://kit:pw@h/db?sslpassword=sslpw&password=qpw",
			want: "postgres://kit:***@h/db?sslpassword=***&password=***",
		},
		{
			name: "repeated credential parameter",
			raw:  "postgres://h/db?password=a&password=b",
			want: "postgres://h/db?password=***&password=***",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redactURL(tc.raw)
			if got != tc.want {
				t.Fatalf("redactURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if strings.Contains(got, "%2A") || strings.Contains(got, "%2a") {
				t.Fatalf("redaction escaped the marker: %q", got)
			}
			if strings.Contains(got, "xxxxx") {
				t.Fatalf("url.Redacted-style marker: %q", got)
			}
			if strings.Contains(got, "s3cret-pw") || strings.Contains(got, "ssl-secret") ||
				strings.Contains(got, "/tmp/") || strings.Contains(got, "qpw") || strings.Contains(got, "sslpw") {
				t.Fatalf("redactURL leaked a secret: %q", got)
			}
		})
	}
}

func TestRedactURLWithoutCredentialsIsUnchanged(t *testing.T) {
	for _, raw := range []string{
		"postgres://u@h/db?sslmode=require",
		"sqlite:///tmp/kit.db",
		"mysql://kit@db.example.test/kit",
	} {
		if got := redactURL(raw); got != raw {
			t.Fatalf("redactURL(%q) = %q", raw, got)
		}
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
