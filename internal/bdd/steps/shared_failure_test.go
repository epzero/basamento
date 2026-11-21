package steps

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFailureRegistryAsksSourcesInOrderAndFirstHolderDecides(t *testing.T) {
	reg := newFailureRegistry()
	var asked []string
	reg.register(callFailurePhrase, "first", func(context.Context, string) (bool, error) {
		asked = append(asked, "first")
		return false, nil
	})
	reg.register(callFailurePhrase, "second", func(context.Context, string) (bool, error) {
		asked = append(asked, "second")
		return true, errors.New("second holds")
	})
	reg.register(callFailurePhrase, "third", func(context.Context, string) (bool, error) {
		asked = append(asked, "third")
		return true, errors.New("third holds")
	})

	err := reg.assert(context.Background(), callFailurePhrase, "not_found")
	if err == nil || err.Error() != "second holds" {
		t.Fatalf("assert error = %v, want second holds", err)
	}
	if got := strings.Join(asked, ","); got != "first,second" {
		t.Fatalf("sources asked = %q, want first,second", got)
	}
}

func TestFailureRegistryNoneHoldsMessages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		phrase string
		code   string
		want   string
	}{
		{"call", callFailurePhrase, "not_found", "call succeeded, want not_found"},
		{"opening", openingFailurePhrase, "unavailable", "opening succeeded, want unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newFailureRegistry()
			reg.register(tc.phrase, "quiet", func(context.Context, string) (bool, error) {
				return false, nil
			})

			err := reg.assert(context.Background(), tc.phrase, tc.code)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("assert error = %v, want %q", err, tc.want)
			}
		})
	}
}
