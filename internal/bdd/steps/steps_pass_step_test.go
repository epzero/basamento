package steps

import (
	"regexp"
	"testing"
)

func TestDurationPassStepPattern(t *testing.T) {
	re := regexp.MustCompile(durationPassStepPattern)

	match := []struct {
		step string
		want string
	}{
		{step: "30 seconds pass", want: "30 seconds"},
		{step: "1 second passes", want: "1 second"},
		{step: "29999 milliseconds pass", want: "29999 milliseconds"},
		{step: "14 minutes 59 seconds pass", want: "14 minutes 59 seconds"},
		{step: "365 days pass", want: "365 days"},
		{step: "0 seconds pass", want: "0 seconds"},
		{step: "-5 seconds pass", want: "-5 seconds"},
		{step: "1 more second passes", want: "1 more second"},
	}
	for _, tt := range match {
		t.Run("match/"+tt.step, func(t *testing.T) {
			m := re.FindStringSubmatch(tt.step)
			if m == nil {
				t.Fatalf("step %q does not match %q", tt.step, durationPassStepPattern)
			}
			if got := m[1]; got != tt.want {
				t.Fatalf("capture group = %q, want %q", got, tt.want)
			}
		})
	}

	noMatch := []string{
		`a run of "timer" starts and 29 minutes pass`,
		`the lease of worker "w1" expires and the grace period passes`,
		"250 milliseconds pass on the clock",
		"time passes",
		"more seconds pass",
	}
	for _, step := range noMatch {
		t.Run("no/"+step, func(t *testing.T) {
			if re.FindStringSubmatch(step) != nil {
				t.Fatalf("step %q must not match %q", step, durationPassStepPattern)
			}
		})
	}
}
