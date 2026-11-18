package conformance

import (
	"fmt"
	"regexp"
	"strconv"
)

var (
	scenarioSummaryRE = regexp.MustCompile(`(\d+)\s+scenarios`)
	passedSummaryRE   = regexp.MustCompile(`(\d+)\s+passed`)
)

// ParseGodogSummary reads godog's pretty-format closing summary.
func ParseGodogSummary(output string) (scenarios, passed int, err error) {
	sm := scenarioSummaryRE.FindStringSubmatch(output)
	if sm == nil {
		return 0, 0, fmt.Errorf("no scenario count in godog output")
	}
	scenarios, err = strconv.Atoi(sm[1])
	if err != nil {
		return 0, 0, err
	}
	pm := passedSummaryRE.FindStringSubmatch(output)
	if pm == nil {
		return scenarios, 0, fmt.Errorf("no passed count in godog output")
	}
	passed, err = strconv.Atoi(pm[1])
	if err != nil {
		return 0, 0, err
	}
	if scenarios == 0 {
		return 0, 0, fmt.Errorf("godog selected zero scenarios")
	}
	return scenarios, passed, nil
}
