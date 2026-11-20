package steps

import (
	"strings"
	"testing"
)

func TestServeArgsKeepsTheCommandFlags(t *testing.T) {
	cases := []struct {
		command string
		want    []string
	}{
		{"kit serve", nil},
		{"kit serve --profile embedded", []string{"--profile", "embedded"}},
		{"kit serve --profile embedded --listen.addr 127.0.0.1:9000", []string{"--profile", "embedded", "--listen.addr", "127.0.0.1:9000"}},
	}
	for _, c := range cases {
		got, err := serveArgs(c.command)
		if err != nil {
			t.Fatalf("serveArgs(%q): %v", c.command, err)
		}
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("serveArgs(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}

func TestServeArgsRejectsAnotherCommand(t *testing.T) {
	for _, command := range []string{"kit config validate", "kit", "serve"} {
		if _, err := serveArgs(command); err == nil {
			t.Errorf("serveArgs(%q) accepted it", command)
		}
	}
}
