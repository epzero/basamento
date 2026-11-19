package bus

import "testing"

func TestValidSubject(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"run.r1.deltas", true},
		{"run.r1.events", true},
		{"", false},
		{"run.*.deltas", false},
		{"run.>", false},
		{"run..deltas", false},
		{"run r1", false},
		{"run.r1.deltas.m2", true},
	}
	for _, tt := range tests {
		if got := ValidSubject(tt.s); got != tt.want {
			t.Errorf("ValidSubject(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestValidKey(t *testing.T) {
	tests := []struct {
		k    string
		want bool
	}{
		{"cursor.r1", true},
		{"lock.r1", true},
		{"", false},
		{".lock", false},
		{"lock.", false},
		{"lock r1", false},
		{"lock.*", false},
		{"lock.>", false},
		{"a/b=c", true},
		{"lock.ré", false},
		{"a..b", false},
	}
	for _, tt := range tests {
		if got := ValidKey(tt.k); got != tt.want {
			t.Errorf("ValidKey(%q) = %v, want %v", tt.k, got, tt.want)
		}
	}
}
