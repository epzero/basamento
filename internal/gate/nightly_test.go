package gate

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

var nightlyNow = time.Date(2025, 11, 20, 12, 0, 0, 0, time.UTC)

func nightlyCreated(offset time.Duration) string {
	return nightlyNow.Add(-offset).Format(time.RFC3339)
}

func TestParseNightlyRuns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantLen int
		wantErr string
	}{
		{name: "empty input", in: "", wantErr: "empty"},
		{name: "whitespace only", in: "  \n  ", wantErr: "empty"},
		{name: "not json", in: "{", wantErr: "JSON"},
		{name: "not array", in: `{"x":1}`, wantErr: "array"},
		{name: "missing createdAt", in: `[{"status":"completed","conclusion":"success","url":"https://example.com/runs/1"}]`, wantErr: "createdAt"},
		{name: "ok array", in: `[{"status":"completed","conclusion":"success","createdAt":"2025-11-20T06:00:00Z","url":"https://example.com/runs/1"}]`, wantLen: 1},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseNightlyRuns([]byte(tc.in))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("len %d, want %d", len(got), tc.wantLen)
			}
		})
	}
}

func TestClassifyNightlySchedules(t *testing.T) {
	t.Parallel()
	const (
		urlOK  = "https://github.com/epzero/basamento/actions/runs/100"
		urlOld = "https://github.com/epzero/basamento/actions/runs/99"
	)
	thirtyFiveH := 35*time.Hour + 59*time.Minute + 59*time.Second
	thirtySixH := 36 * time.Hour
	thirtySixH1s := 36*time.Hour + time.Second
	fiveH := 5 * time.Hour

	run := func(status, conclusion, created, url string) NightlyRun {
		return NightlyRun{Status: status, Conclusion: conclusion, CreatedAt: created, URL: url}
	}

	tests := []struct {
		name string
		runs []NightlyRun
		want NightlyStatus
	}{
		{name: "empty array stale", runs: nil, want: NightlyStale},
		{name: "only in progress stale", runs: []NightlyRun{
			run("in_progress", "", nightlyCreated(time.Hour), urlOK),
			run("queued", "", nightlyCreated(2*time.Hour), urlOld),
		}, want: NightlyStale},
		{name: "ok fresh success", runs: []NightlyRun{
			run("completed", "success", nightlyCreated(fiveH), urlOK),
		}, want: NightlyOK},
		{name: "ok with newer in progress on top", runs: []NightlyRun{
			run("in_progress", "", nightlyCreated(time.Hour), "https://example.com/runs/200"),
			run("completed", "success", nightlyCreated(fiveH), urlOK),
		}, want: NightlyOK},
		{name: "stale just past 36h", runs: []NightlyRun{
			run("completed", "success", nightlyCreated(thirtySixH1s), urlOK),
		}, want: NightlyStale},
		{name: "ok at exactly 36h", runs: []NightlyRun{
			run("completed", "success", nightlyCreated(thirtySixH), urlOK),
		}, want: NightlyOK},
		{name: "ok just inside 36h", runs: []NightlyRun{
			run("completed", "success", nightlyCreated(thirtyFiveH), urlOK),
		}, want: NightlyOK},
	}

	failedConclusions := []string{
		"failure", "cancelled", "timed_out", "skipped", "neutral",
		"action_required", "startup_failure",
	}
	for _, c := range failedConclusions {
		c := c
		tests = append(tests, struct {
			name string
			runs []NightlyRun
			want NightlyStatus
		}{
			name: "failed " + c,
			runs: []NightlyRun{run("completed", c, nightlyCreated(fiveH), urlOK)},
			want: NightlyFailed,
		})
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyNightlySchedules(tc.runs, nightlyNow)
			if got.Status != tc.want {
				t.Fatalf("status %q, want %q (detail %q)", got.Status, tc.want, got.Detail)
			}
		})
	}
}

func TestNightlyRunID(t *testing.T) {
	t.Parallel()
	if got := NightlyRunID("https://github.com/org/repo/actions/runs/12345678"); got != "12345678" {
		t.Fatalf("got %q", got)
	}
	if got := NightlyRunID("https://example.com/no-id"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestEvaluateNightly(t *testing.T) {
	t.Parallel()
	okJSON := `[{"status":"completed","conclusion":"success","createdAt":"` + nightlyCreated(5*time.Hour) + `","url":"https://github.com/epzero/basamento/actions/runs/42"}]`
	res, err := EvaluateNightly(bytes.NewReader([]byte(okJSON)), nightlyNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != NightlyOK || res.ExitCode() != 0 {
		t.Fatalf("%+v", res)
	}
	line := FormatNightlyLine(res)
	if !strings.HasPrefix(line, "nightly: ok:") {
		t.Fatalf("%q", line)
	}

	_, err = EvaluateNightly(bytes.NewReader(nil), nightlyNow)
	if err == nil {
		t.Fatal("expected parse error")
	}
}
