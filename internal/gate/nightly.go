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
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	CreatedAt  string `json:"createdAt"`
	URL        string `json:"url"`
}

// NightlyStatus is the schedule health: ok, failed, or stale.
type NightlyStatus string

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

// ParseNightlyRuns decodes the JSON array gh prints on standard output.
// The slice order is gh's: newest run first. An empty array decodes to a
// non-nil empty slice.
func ParseNightlyRuns(data []byte) ([]NightlyRun, error) {
	trim := bytes.TrimSpace(data)
	if len(trim) == 0 {
		return nil, fmt.Errorf("nightly: empty input")
	}
	if trim[0] != '[' {
		return nil, fmt.Errorf("nightly: input is not a JSON array")
	}
	var runs []NightlyRun
	if err := json.Unmarshal(trim, &runs); err != nil {
		return nil, fmt.Errorf("nightly: input is not JSON: %w", err)
	}
	for i, r := range runs {
		if strings.TrimSpace(r.CreatedAt) == "" {
			return nil, fmt.Errorf("nightly: run %d missing createdAt", i)
		}
	}
	return runs, nil
}

// ClassifyNightlySchedules picks the newest completed scheduled run from runs
// (gh order: newest first; this function does not reorder) and classifies the
// schedule at now. Only conclusion success counts as ok; only status
// completed counts as finished.
func ClassifyNightlySchedules(runs []NightlyRun, now time.Time) NightlyOutcome {
	var latest *NightlyRun
	for i := range runs {
		r := &runs[i]
		if !strings.EqualFold(r.Status, "completed") {
			continue
		}
		latest = r
		break
	}
	if latest == nil {
		return NightlyOutcome{
			Status: NightlyStale,
			Detail: "no completed scheduled run",
		}
	}
	created, err := time.Parse(time.RFC3339, latest.CreatedAt)
	if err != nil {
		return NightlyOutcome{
			Status: NightlyStale,
			Detail: fmt.Sprintf("newest completed run has unreadable createdAt %q", latest.CreatedAt),
			Run:    latest,
		}
	}
	age := now.Sub(created)
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
	return fmt.Sprintf("%s at %s", r.URL, r.CreatedAt)
}

// NightlyRunID returns the numeric Actions run id from a run URL, for ticket
// deduplication titles.
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
	if rest == "" {
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

// ExitCode maps the outcome to the nightly subcommand exit status.
func (o NightlyOutcome) ExitCode() int {
	switch o.Status {
	case NightlyOK:
		return 0
	default:
		return 1
	}
}
