package temporal

import (
	"fmt"
	"testing"
)

func TestSupportedServerVersionMatchesPinnedMinors(t *testing.T) {
	want := fmt.Sprintf("%d.%d", supportedServerMajor, supportedServerMinor)
	if SupportedServerVersion != want {
		t.Fatalf("SupportedServerVersion=%q want %q", SupportedServerVersion, want)
	}
}

func TestParseServerVersionMajorMinorTable(t *testing.T) {
	// Odd cases: 1.032.0 rejects leading zero on minor; spaces anywhere reject;
	// 1.32. trims trailing dot; empty and non-numeric reject; pre-release stripped.
	cases := []struct {
		in   string
		ok   bool
		maj  int
		min  int
		supp bool
	}{
		{"1.32.2", true, 1, 32, true},
		{"1.32.0", true, 1, 32, true},
		{"1.32.99", true, 1, 32, true},
		{"v1.32.2", true, 1, 32, true},
		{"V1.32.2", false, 0, 0, false},
		{"1.32.2-rc.1", true, 1, 32, true},
		{"1.32", true, 1, 32, true},
		{"1.31.9", true, 1, 31, false},
		{"1.33.0", true, 1, 33, false},
		{"2.32.0", true, 2, 32, false},
		{"0.32.0", true, 0, 32, false},
		{"1.320.0", true, 1, 320, false},
		{"1.3", true, 1, 3, false},
		{"1", false, 0, 0, false},
		{"", false, 0, 0, false},
		{"x.y.z", false, 0, 0, false},
		{"1.32.", false, 0, 0, false},
		{" 1.32.2", false, 0, 0, false},
		{"1.32.2 ", false, 0, 0, false},
		{"1.032.0", false, 0, 0, false},
		{"-1.32.0", false, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			maj, min, ok := parseServerVersionMajorMinor(tc.in)
			if ok != tc.ok {
				t.Fatalf("parse ok=%v want %v", ok, tc.ok)
			}
			if !ok {
				if serverVersionSupported(tc.in) {
					t.Fatal("supported unexpectedly")
				}
				return
			}
			if maj != tc.maj || min != tc.min {
				t.Fatalf("parsed %d.%d want %d.%d", maj, min, tc.maj, tc.min)
			}
			if serverVersionSupported(tc.in) != tc.supp {
				t.Fatalf("supported=%v want %v", serverVersionSupported(tc.in), tc.supp)
			}
		})
	}
}
