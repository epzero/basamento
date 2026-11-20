package steps

import (
	"strings"
	"testing"
	"time"
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

func TestShutdownTimerFiresOnlyAfterItsDuration(t *testing.T) {
	timer := newShutdownTimer()
	fired := timer.After(5 * time.Second)
	timer.advance(time.Second)
	select {
	case <-fired:
		t.Fatal("the timer fired after 1s of a 5s timeout")
	default:
	}
	timer.advance(4 * time.Second)
	select {
	case <-fired:
	default:
		t.Fatal("the timer did not fire after 5s")
	}
}

func TestShutdownTimerFiresWhenTheAdvanceComesFirst(t *testing.T) {
	timer := newShutdownTimer()
	timer.advance(5 * time.Second)
	fired := timer.After(5 * time.Second)
	select {
	case <-fired:
	default:
		t.Fatal("an advance before After was lost")
	}
}
