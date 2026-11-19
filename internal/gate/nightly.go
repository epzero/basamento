package gate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// NightlyStaleAfter is how old the newest completed scheduled run may be
// before the schedule is treated as stale.
const NightlyStaleAfter = 36 * time.Hour

// NightlyRun is one row from `gh run list --json conclusion,status,createdAt,url`.
type NightlyRun struct {
	Conclusion string    `json:"conclusion"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"createdAt"`
	URL        string    `json:"url"`
}

// NightlyStatus is the schedule health.
type NightlyStatus string

// NightlyOK: newest completed scheduled run succeeded and was created within
// the stale window (36 hours).
// NightlyFailed: newest completed run did not conclude success.
// NightlyStale: no scheduled run (no completed row), or the newest completed run
// was created too long ago.
const (
	NightlyOK     NightlyStatus = "ok"
	NightlyFailed NightlyStatus = "failed"
	NightlyStale  NightlyStatus = "stale"
)

// NightlyOutcome is the classification of the scheduled nightly runs plus the
// run it was based on, when there is one.
type NightlyOutcome struct {
	Status NightlyStatus
	Detail string
	Run    *NightlyRun
}

const nightlyGHJSONHint = "pipe output of gh run list --json conclusion,status,createdAt,url"

// ParseNightlyRuns decodes the JSON array gh prints on standard output.
// The slice order is gh's: newest run first. An empty array decodes to a
// non-nil empty slice.
func ParseNightlyRuns(data []byte) ([]NightlyRun, error) {
	trim := bytes.TrimSpace(data)
	if len(trim) == 0 {
		return nil, fmt.Errorf("nightly: empty input; %s", nightlyGHJSONHint)
	}
	if trim[0] != '[' {
		return nil, fmt.Errorf("nightly: input is not a JSON array; %s", nightlyGHJSONHint)
	}
	var runs []NightlyRun
	if err := json.Unmarshal(trim, &runs); err != nil {
		return nil, fmt.Errorf("nightly: input is not JSON: %w; %s", err, nightlyGHJSONHint)
	}
	for i, r := range runs {
		if r.CreatedAt.IsZero() {
			return nil, fmt.Errorf("nightly: run %d missing createdAt; %s", i, nightlyGHJSONHint)
		}
	}
	return runs, nil
}

// ClassifyNightlySchedules picks the completed scheduled run with the
// greatest CreatedAt from runs, whatever their order, and classifies the
// schedule at now. Only conclusion success counts as ok; only status
// completed counts as finished.
func ClassifyNightlySchedules(runs []NightlyRun, now time.Time) NightlyOutcome {
	var latest *NightlyRun
	for i := range runs {
		r := &runs[i]
		if !strings.EqualFold(r.Status, "completed") {
			continue
		}
		if latest == nil || r.CreatedAt.After(latest.CreatedAt) {
			latest = r
		}
	}
	if latest == nil {
		return NightlyOutcome{
			Status: NightlyStale,
			Detail: "no scheduled run",
		}
	}
	age := now.Sub(latest.CreatedAt)
	if age > NightlyStaleAfter {
		return NightlyOutcome{
			Status: NightlyStale,
			Detail: nightlyRunDetail(latest),
			Run:    latest,
		}
	}
	if latest.Conclusion != "success" {
		return NightlyOutcome{
			Status: NightlyFailed,
			Detail: nightlyRunDetail(latest),
			Run:    latest,
		}
	}
	return NightlyOutcome{
		Status: NightlyOK,
		Detail: nightlyRunDetail(latest),
		Run:    latest,
	}
}

func nightlyRunDetail(r *NightlyRun) string {
	id := NightlyRunID(r.URL)
	if id == "" {
		id = "unknown"
	}
	return fmt.Sprintf("run %s %s created %s", id, r.URL, r.CreatedAt.UTC().Format(time.RFC3339))
}

// NightlyRunID returns the numeric Actions run id from a run URL, for ticket
// titles. A URL whose run segment is empty or not all digits yields "" so the
// caller falls back to unknown rather than carry arbitrary text into a title.
func NightlyRunID(url string) string {
	const marker = "/runs/"
	i := strings.LastIndex(url, marker)
	if i < 0 {
		return ""
	}
	rest := url[i+len(marker):]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	if rest == "" || strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return ""
	}
	return rest
}

// EvaluateNightly reads gh's JSON from r, classifies it at now, and returns
// a parse error when the input is unusable.
func EvaluateNightly(r io.Reader, now time.Time) (NightlyOutcome, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return NightlyOutcome{}, fmt.Errorf("nightly: read input: %w", err)
	}
	runs, err := ParseNightlyRuns(data)
	if err != nil {
		return NightlyOutcome{}, err
	}
	return ClassifyNightlySchedules(runs, now), nil
}

// FormatNightlyLine prints one summary line for the gate subcommand.
func FormatNightlyLine(o NightlyOutcome) string {
	return fmt.Sprintf("nightly: %s: %s", o.Status, o.Detail)
}

// ExitCode returns the process exit status for a classified outcome: 0 for ok,
// 1 for failed or stale. The nightly command exits 2 on unusable input.
func (o NightlyOutcome) ExitCode() int {
	switch o.Status {
	case NightlyOK:
		return 0
	default:
		return 1
	}
}
