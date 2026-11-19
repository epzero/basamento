// Command codegen is the codegen preset's binary. It exposes the preset's
// flows, their version identity and the version lifecycle. A run's model,
// sandbox and git host are ports; the gate drives them with the scripted doubles
// in internal/bdd/codegen, and a deployment supplies live adapters.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
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
  eval --scores FILE print the recorded eval means
  propose --version ID --scores FILE [--current ID]
                     run compile and eval for a proposed version
  resolve --version ID --scores FILE [--current ID] --decision approved|rejected
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

func eval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	scores := fs.String("scores", "", "recorded scores file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	recorded, err := readScores(*scores)
	if err != nil {
		return err
	}
	src := codegen.DefaultSources()
	for _, caseID := range []string{"plan-modules", "iterate-minimal"} {
		for _, metric := range sortedMetrics(recorded[caseID]) {
			mean, err := src.Mean(caseID, metric)
			if err != nil {
				return err
			}
			fmt.Printf("%s %s %.2f\n", caseID, metric, mean)
		}
	}
	return nil
}

func propose(args []string) error {
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
	reg := codegen.NewRegistry([]string{"plan-modules", "iterate-minimal"})
	if *current != "" {
		reg.Add(*current, codegen.DefaultSources(), recorded[*current])
		reg.AcceptInitial(*current)
	}
	reg.Add(*id, codegen.DefaultSources(), recorded[*id])
	report(reg.Propose(*id))
	return nil
}

func resolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	id := fs.String("version", "", "version id")
	current := fs.String("current", "", "the current version id")
	scores := fs.String("scores", "", "recorded scores file")
	decision := fs.String("decision", "", "approved|rejected")
	if err := fs.Parse(args); err != nil {
		return err
	}
	recorded, err := readScores(*scores)
	if err != nil {
		return err
	}
	reg := codegen.NewRegistry([]string{"plan-modules", "iterate-minimal"})
	reg.Add(*current, codegen.DefaultSources(), recorded[*current])
	reg.AcceptInitial(*current)
	reg.Add(*id, codegen.DefaultSources(), recorded[*id])
	reg.Propose(*id)
	report(reg.Resolve(*id, *decision))
	return nil
}

func report(v *codegen.Version) {
	if v == nil {
		fmt.Println("no such version")
		return
	}
	fmt.Printf("version %s %s\n", v.ID, v.Status)
	if v.Code != "" {
		fmt.Printf("code %s", v.Code)
		if v.Case != "" {
			fmt.Printf(" case %s", v.Case)
		}
		fmt.Println()
	}
	if v.GateOpen {
		fmt.Println("review gate open")
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
