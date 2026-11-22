// Command codegen is the codegen preset's binary. It exposes the preset's
// flows, their version identity and the version lifecycle. A run's model,
// sandbox and git host are ports; the gate drives them with the scripted doubles
// in internal/bdd/codegen, and a deployment supplies live adapters.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/epzero/basamento/core"
	flowir "github.com/epzero/basamento/flow/ir"
	codegen "github.com/epzero/basamento/presets/codegen"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "flows":
		err = flows()
	case "version":
		err = version()
	case "eval":
		err = eval(os.Args[2:])
	case "propose":
		err = propose(os.Args[2:])
	case "resolve":
		err = resolve(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "codegen:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: codegen <command>

  flows              print the flows and their canonical hashes
  version            print the preset version hash
  eval --scores FILE print the recorded eval means (case -> metric -> score)
  propose --version ID --scores FILE [--current ID]
                     run compile and eval for a proposed version; the durable
                     record is <scores>.versions.json
  resolve --version ID --scores FILE --decision approved|rejected
                     record a person's decision on a version's review gate`)
}

func flows() error {
	src := codegen.DefaultSources()
	names := make([]string, 0, len(src.Flows))
	for name := range src.Flows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		hash, err := flowir.Hash(src.Flows[name])
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", name, hash)
	}
	return nil
}

func version() error {
	src := codegen.DefaultSources()
	if err := src.Compile(); err != nil {
		return err
	}
	id, err := src.Version()
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

func eval(args []string) error { return evalTo(os.Stdout, args) }

func evalTo(w io.Writer, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	scores := fs.String("scores", "", "recorded scores file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	recorded, err := readScores(*scores)
	if err != nil {
		return err
	}
	return writeEvalMeans(w, recorded)
}

// writeEvalMeans prints the values the scores file records, not an embedded
// mean. A case's value is the recorded mean for its metric.
func writeEvalMeans(w io.Writer, recorded map[string]map[string]int) error {
	printed := 0
	for _, caseID := range codegen.Suite() {
		scores, ok := recorded[caseID]
		if !ok {
			continue
		}
		for _, metric := range sortedMetrics(scores) {
			fmt.Fprintf(w, "%s %s %.2f\n", caseID, metric, float64(scores[metric]))
			printed++
		}
	}
	if printed == 0 {
		return core.NewError(core.CodeInvalidArgument, "the scores file has no case scores", nil)
	}
	return nil
}

func propose(args []string) error { return proposeTo(os.Stdout, args) }

func proposeTo(w io.Writer, args []string) error {
	fs := flag.NewFlagSet("propose", flag.ContinueOnError)
	id := fs.String("version", "", "version id")
	current := fs.String("current", "", "the current version id")
	scores := fs.String("scores", "", "recorded scores file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return core.NewError(core.CodeInvalidArgument, "--version is required", nil)
	}
	recorded, err := readScores(*scores)
	if err != nil {
		return err
	}
	if _, ok := recorded[*id]; !ok {
		return core.NewError(core.CodeInvalidArgument, fmt.Sprintf("--version %q has no recorded scores in %s", *id, *scores), nil)
	}
	if *current != "" {
		if _, ok := recorded[*current]; !ok {
			return core.NewError(core.CodeInvalidArgument, fmt.Sprintf("--current %q has no recorded scores in %s", *current, *scores), nil)
		}
	}
	recordPath := codegen.RecordPath(*scores)
	reg, err := codegen.LoadRegistry(recordPath, codegen.Suite())
	if err != nil {
		return err
	}
	if *current != "" {
		if reg.Current != "" && reg.Current != *current {
			return core.NewError(core.CodeConflict, fmt.Sprintf("the record's current version is %q, not %q", reg.Current, *current), nil)
		}
		if reg.Current == "" {
			if v := reg.Add(*current, codegen.DefaultSources(), recorded[*current]); v.Status == codegen.StatusRejected {
				return core.NewError(core.CodeConflict, fmt.Sprintf("the recorded current version %q is rejected", *current), nil)
			}
			reg.AcceptInitial(*current)
		}
	}
	reg.Add(*id, codegen.DefaultSources(), recorded[*id])
	report(w, reg.Propose(*id))
	return codegen.SaveRegistry(recordPath, reg)
}

func resolve(args []string) error { return resolveTo(os.Stdout, args) }

func resolveTo(w io.Writer, args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	id := fs.String("version", "", "version id")
	scores := fs.String("scores", "", "recorded scores file (its sibling record is read)")
	decision := fs.String("decision", "", "approved|rejected")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return core.NewError(core.CodeInvalidArgument, "--version is required", nil)
	}
	if err := validateDecision(*decision); err != nil {
		return err
	}
	if *scores == "" {
		return core.NewError(core.CodeInvalidArgument, "--scores is required", nil)
	}
	reg, err := codegen.LoadRegistry(codegen.RecordPath(*scores), codegen.Suite())
	if err != nil {
		return err
	}
	if reg.Get(*id) == nil {
		return core.NewError(core.CodeNotFound, fmt.Sprintf("version %q is not recorded; run propose first", *id), nil)
	}
	v, err := reg.Resolve(*id, *decision)
	if err != nil {
		return err
	}
	report(w, v)
	return codegen.SaveRegistry(codegen.RecordPath(*scores), reg)
}

func validateDecision(decision string) error {
	switch decision {
	case "approved", "rejected":
		return nil
	case "":
		return core.NewError(core.CodeInvalidArgument, "--decision is required (approved or rejected)", nil)
	default:
		return core.NewError(core.CodeInvalidArgument, fmt.Sprintf("unknown --decision %q; use approved or rejected", decision), nil)
	}
}

func report(w io.Writer, v *codegen.Version) {
	if v == nil {
		fmt.Fprintln(w, "no such version")
		return
	}
	fmt.Fprintf(w, "version %s %s\n", v.ID, v.Status)
	if v.Code != "" {
		fmt.Fprintf(w, "code %s", v.Code)
		if v.Case != "" {
			fmt.Fprintf(w, " case %s", v.Case)
		}
		fmt.Fprintln(w)
	}
	if v.GateOpen {
		fmt.Fprintln(w, "review gate open")
	}
}

func sortedMetrics(scores map[string]int) []string {
	metrics := make([]string, 0, len(scores))
	for metric := range scores {
		metrics = append(metrics, metric)
	}
	sort.Strings(metrics)
	return metrics
}

func readScores(path string) (map[string]map[string]int, error) {
	if path == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "--scores is required", nil)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]map[string]int
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "scores: "+err.Error(), nil)
	}
	return out, nil
}
