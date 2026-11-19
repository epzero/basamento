package gate

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

var nightlyNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func nightlyAt(offset time.Duration) time.Time {
	return nightlyNow.Add(-offset)
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
		{name: "missing createdAt", in: `[{"status":"completed","conclusion":"success","url":"https://example.com/runs/1"}]`, wantErr: "missing createdAt"},
		{name: "unreadable createdAt", in: `[{"status":"completed","conclusion":"success","createdAt":"yesterday","url":"https://example.com/runs/1"}]`, wantErr: "gh run list"},
		{name: "ok array", in: `[{"status":"completed","conclusion":"success","createdAt":"2026-11-20T06:00:00Z","url":"https://example.com/runs/1"}]`, wantLen: 1},
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

func TestEvaluateNightly_parseErrors(t *testing.T) {
	t.Parallel()
	cases := []string{
		`[{"status":"completed","conclusion":"success","url":"https://example.com/runs/1"}]`,
		`[{"status":"completed","conclusion":"success","createdAt":"yesterday","url":"https://example.com/runs/1"}]`,
	}
	for _, in := range cases {
		in := in
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := EvaluateNightly(strings.NewReader(in), nightlyNow)
			if err == nil {
				t.Fatal("expected error")
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

	run := func(status, conclusion string, created time.Time, url string) NightlyRun {
		return NightlyRun{Status: status, Conclusion: conclusion, CreatedAt: created, URL: url}
	}

	tests := []struct {
		name string
		runs []NightlyRun
		want NightlyStatus
	}{
		{name: "empty array stale", runs: nil, want: NightlyStale},
		{name: "only in progress stale", runs: []NightlyRun{
			run("in_progress", "", nightlyAt(time.Hour), urlOK),
			run("queued", "", nightlyAt(2*time.Hour), urlOld),
		}, want: NightlyStale},
		{name: "ok fresh success", runs: []NightlyRun{
			run("completed", "success", nightlyAt(fiveH), urlOK),
		}, want: NightlyOK},
		{name: "ok with newer in progress on top", runs: []NightlyRun{
			run("in_progress", "", nightlyAt(time.Hour), "https://example.com/runs/200"),
			run("completed", "success", nightlyAt(fiveH), urlOK),
		}, want: NightlyOK},
		{name: "stale just past 36h", runs: []NightlyRun{
			run("completed", "success", nightlyAt(thirtySixH1s), urlOK),
		}, want: NightlyStale},
		{name: "ok at exactly 36h", runs: []NightlyRun{
			run("completed", "success", nightlyAt(thirtySixH), urlOK),
		}, want: NightlyOK},
		{name: "ok just inside 36h", runs: []NightlyRun{
			run("completed", "success", nightlyAt(thirtyFiveH), urlOK),
		}, want: NightlyOK},
	}

	failedConclusions := []struct {
		name       string
		conclusion string
	}{
		{name: "failed empty conclusion", conclusion: ""},
		{name: "failed failure", conclusion: "failure"},
		{name: "failed cancelled", conclusion: "cancelled"},
		{name: "failed timed_out", conclusion: "timed_out"},
		{name: "failed skipped", conclusion: "skipped"},
		{name: "failed neutral", conclusion: "neutral"},
		{name: "failed action_required", conclusion: "action_required"},
		{name: "failed startup_failure", conclusion: "startup_failure"},
	}
	for _, c := range failedConclusions {
		c := c
		tests = append(tests, struct {
			name string
			runs []NightlyRun
			want NightlyStatus
		}{
			name: c.name,
			runs: []NightlyRun{run("completed", c.conclusion, nightlyAt(fiveH), urlOK)},
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

func TestFormatNightlyLine(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 11, 19, 11, 40, 22, 0, time.UTC)
	url := "https://github.com/epzero/basamento/actions/runs/44516903074"
	run := &NightlyRun{
		Status:     "completed",
		Conclusion: "failure",
		CreatedAt:  created,
		URL:        url,
	}
	detail := nightlyRunDetail(run)
	wantFailed := "nightly: failed: run 44516903074 https://github.com/epzero/basamento/actions/runs/44516903074 created 2026-11-19T11:40:22Z"
	if got := FormatNightlyLine(NightlyOutcome{Status: NightlyFailed, Detail: detail, Run: run}); got != wantFailed {
		t.Fatalf("got %q\nwant %q", got, wantFailed)
	}

	wantStaleNone := "nightly: stale: no completed scheduled run"
	if got := FormatNightlyLine(ClassifyNightlySchedules(nil, nightlyNow)); got != wantStaleNone {
		t.Fatalf("got %q", got)
	}

	noID := &NightlyRun{
		Status:     "completed",
		Conclusion: "success",
		CreatedAt:  created,
		URL:        "https://example.com/no-run-path",
	}
	detailUnknown := nightlyRunDetail(noID)
	if !strings.Contains(detailUnknown, "run unknown") {
		t.Fatalf("detail %q", detailUnknown)
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

func TestNightlyOutcomeExitCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status NightlyStatus
		want   int
	}{
		{status: NightlyOK, want: 0},
		{status: NightlyFailed, want: 1},
		{status: NightlyStale, want: 1},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			if got := (NightlyOutcome{Status: tc.status}).ExitCode(); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestEvaluateNightly(t *testing.T) {
	t.Parallel()
	okJSON := `[{"status":"completed","conclusion":"success","createdAt":"2026-11-20T07:00:00Z","url":"https://github.com/epzero/basamento/actions/runs/42"}]`
	res, err := EvaluateNightly(bytes.NewReader([]byte(okJSON)), nightlyNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != NightlyOK || res.ExitCode() != 0 {
		t.Fatalf("%+v", res)
	}
	line := FormatNightlyLine(res)
	want := "nightly: ok: run 42 https://github.com/epzero/basamento/actions/runs/42 created 2026-11-20T07:00:00Z"
	if line != want {
		t.Fatalf("got %q want %q", line, want)
	}

	_, err = EvaluateNightly(bytes.NewReader(nil), nightlyNow)
	if err == nil {
		t.Fatal("expected parse error")
	}
}
