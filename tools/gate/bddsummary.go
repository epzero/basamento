package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type bddSkipReport struct {
	Outcome        string         `json:"outcome"`
	PostgresServer string         `json:"postgres_server"`
	SkipRuns       map[string]int `json:"skip_runs"`
}

func printBDDSkips(root string) error {
	path := filepath.Join(root, ".gate", "bdd.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("bdd-skips: %w", err)
	}
	var rep bddSkipReport
	if err := json.Unmarshal(b, &rep); err != nil {
		return fmt.Errorf("bdd-skips: %w", err)
	}
	total := 0
	for _, n := range rep.SkipRuns {
		total += n
	}
	line := fmt.Sprintf("gate: scenario runs skipped: %d", total)
	if total > 0 {
		reasons := make([]string, 0, len(rep.SkipRuns))
		for r := range rep.SkipRuns {
			reasons = append(reasons, r)
		}
		sort.Strings(reasons)
		var parts []string
		for _, r := range reasons {
			parts = append(parts, fmt.Sprintf("%s: %d", r, rep.SkipRuns[r]))
		}
		line += " (" + strings.Join(parts, ", ") + ")"
	}
	fmt.Println(line)
	return nil
}