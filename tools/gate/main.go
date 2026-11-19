// Command gate runs the repository QA gate checks. Usage:
//
//	gate agent new -harness <tool> -role <role> [-issue WP-L3-05] [-branch <pushed branch>] [-parent <id>]   # identity + worktree
//	gate agent whoami | list | release [-instance <id>]
//	gate agent status <status> [-note <text>] [-issue <id>]   # how this session ended; the next one reads it
//	gate wp [-state ready] [-json]   # every work package: done, in-progress, ready or blocked
//	gate docs [-write]       # generated diagrams match layering.yaml and the roadmap
//	gate review plan [-base <ref>] [-head <ref>]   # risk tier, review depth and REVIEW.md files of a change
//	gate layering
//	gate gomod                  # every tracked go.mod names the same go directive
//	gate spec lint            # lint + traceability
//	gate spec lock            # every scenario approved at its current text
//	gate spec trace [-json]   # requirement → scenario matrix
//	gate spec approve -ids A,B|-unapproved -tier P2 -author <id> [-reviewer <id>] [-note …] [-ref <url>]
//	gate spec human -id A -status accepted|rejected -by <github-login> [-note …]
//	gate spec queue           # human review queue, most critical first
//	gate release-check        # P0 scenarios need human acceptance before a release
//	gate redgreen -base <ref> # newly active scenarios must fail at base
//	gate bdd-skips            # print gate: scenario runs skipped: … from .gate/bdd.json
//
// See docs/engineering/qa-gate.md and docs/engineering/agent-workflow.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/epzero/basamento/internal/gate"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	root, err := os.Getwd()
	must(err)
	switch os.Args[1] {
	case "agent":
		agentCmd(root, os.Args[2:])
	case "review":
		if len(os.Args) < 3 || os.Args[2] != "plan" {
			usage()
		}
		fs := flag.NewFlagSet("review plan", flag.ExitOnError)
		base := fs.String("base", "main", "ref the change is reviewed against")
		head := fs.String("head", "", "branch or commit under review (default: the working tree)")
		asJSON := fs.Bool("json", false, "print JSON")
		must(fs.Parse(os.Args[3:]))
		files, err := gate.ChangedFiles(root, *base, *head)
		must(err)
		plan, err := gate.PlanReview(root, *base, *head, files)
		must(err)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			must(enc.Encode(plan))
			return
		}
		fmt.Printf("risk tier: %s (see docs/engineering/code-review.md)\nreview docs: %s\n", plan.Tier, strings.Join(plan.ReviewDocs, ", "))
		for _, f := range files {
			fmt.Printf("  %-7s %s\n", plan.Files[f], f)
		}
	case "wp":
		fs := flag.NewFlagSet("wp", flag.ExitOnError)
		state := fs.String("state", "", "only this state: done, in-progress, ready or blocked")
		asJSON := fs.Bool("json", false, "print JSON")
		must(fs.Parse(os.Args[2:]))
		all, err := gate.WorkPackages(root)
		must(err)
		wps := all[:0]
		for _, w := range all {
			if *state == "" || w.State == *state {
				wps = append(wps, w)
			}
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			must(enc.Encode(wps))
			return
		}
		for _, w := range wps {
			detail := w.Status
			if w.State == gate.WPBlocked {
				detail = "waits for " + strings.Join(w.Waiting, ", ")
			}
			fmt.Printf("%-9s %-12s %-34s %s\n", w.ID, w.State, detail, w.Title)
		}
	case "docs":
		fs := flag.NewFlagSet("docs", flag.ExitOnError)
		write := fs.Bool("write", false, "refresh generated blocks instead of checking them")
		must(fs.Parse(os.Args[2:]))
		stale, err := gate.Docs(root, *write)
		must(err)
		report("docs", stale)
	case "layering":
		errs, err := gate.Layering(root)
		must(err)
		report("layering", errs)
	case "gomod":
		errs, err := gate.CheckSameGoModLine(root)
		must(err)
		report("gomod", errs)
	case "module-roots":
		roots, err := gate.ModuleRoots(root)
		must(err)
		for _, r := range roots {
			fmt.Println(r)
		}
	case "spec":
		if len(os.Args) < 3 {
			usage()
		}
		specCmd(root, os.Args[2], os.Args[3:])
	case "release-check":
		l, err := gate.ReadLedger(root)
		must(err)
		report("release-check", gate.ReleaseCheck(l))
	case "bdd-skips":
		must(printBDDSkips(root))
	case "redgreen":
		fs := flag.NewFlagSet("redgreen", flag.ExitOnError)
		base := fs.String("base", "", "git ref to compare against")
		must(fs.Parse(os.Args[2:]))
		if *base == "" {
			fmt.Println("redgreen: no -base given; skipped")
			return
		}
		act, errs, err := gate.RedGreen(root, *base)
		must(err)
		fmt.Printf("redgreen: activated scenarios: %v\n", act)
		report("redgreen", errs)
	default:
		usage()
	}
}

func agentCmd(root string, args []string) {
	if len(args) < 1 {
		usage()
	}
	switch args[0] {
	case "new":
		fs := flag.NewFlagSet("agent new", flag.ExitOnError)
		harness := fs.String("harness", "", "tool hosting this session: claude-code, cursor, codex, antigravity, …")
		role := fs.String("role", "", "spec-author | spec-reviewer | implementer | code-reviewer | arbiter | dispatcher")
		issue := fs.String("issue", "", "the work item: a work package id (WP-L3-05) or a tracker key (ABC-12)")
		parent := fs.String("parent", "", "identity of the lead session that spawned this one")
		base := fs.String("base", "main", "branch the new worktree starts from")
		branch := fs.String("branch", "", "continue this pushed branch (work that began in another clone)")
		noWT := fs.Bool("no-worktree", false, "stay in the current checkout (default for dispatchers)")
		must(fs.Parse(args[1:]))
		s, err := gate.NewSession(root, gate.NewSessionRequest{Harness: *harness, Role: *role, Issue: *issue, Parent: *parent,
			Base: *base, Branch: *branch, NoWorktree: *noWT || *role == gate.RoleDispatcher})
		must(err)
		fmt.Printf("identity: %s\n", s.Identity)
		if s.Reviews != "" {
			fmt.Printf("reviews:  %s (detached snapshot; do not commit)\n", s.Reviews)
		} else {
			fmt.Printf("branch:   %s\n", s.Branch)
		}
		fmt.Printf("worktree: %s\n", s.Worktree)
		if p := s.Previous; p != nil && p.Status != "" {
			fmt.Printf("\nhand-off from %s: %s\n%s\n", p.Identity, p.Status, p.Note)
		}
		if s.Worktree != root {
			fmt.Printf("\nWork only inside the worktree from now on: cd %s\n", s.Worktree)
		}
	case "whoami":
		id, err := gate.CurrentIdentity(root)
		must(err)
		fmt.Println(id)
	case "list":
		fs := flag.NewFlagSet("agent list", flag.ExitOnError)
		base := fs.String("base", "main", "branch to check merges against")
		asJSON := fs.Bool("json", false, "print JSON")
		must(fs.Parse(args[1:]))
		sessions, err := gate.ListSessions(root, *base)
		must(err)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			must(enc.Encode(sessions))
			return
		}
		for _, s := range sessions {
			state := "open"
			if s.Merged {
				state = "merged"
			}
			fmt.Printf("%-38s %-10s %-8s %-24s %-24s %s\n", s.Identity, s.Issue, state, s.Status, s.Branch, s.Worktree)
		}
	case "status":
		if len(args) < 2 {
			usage()
		}
		fs := flag.NewFlagSet("agent status", flag.ExitOnError)
		note := fs.String("note", "", "what the next session or the lead needs to know")
		issue := fs.String("issue", "", "set it on this work item's session instead of this worktree's (the lead)")
		must(fs.Parse(args[2:]))
		s, err := gate.SetStatus(root, *issue, args[1], *note)
		must(err)
		fmt.Printf("%s %s: %s\n", s.Issue, s.Identity, s.Status)
	case "release":
		fs := flag.NewFlagSet("agent release", flag.ExitOnError)
		instance := fs.String("instance", "", "instance of the session to release (default: this session)")
		base := fs.String("base", "main", "branch the work must be merged into")
		force := fs.Bool("force", false, "remove even if not merged (discards the work)")
		must(fs.Parse(args[1:]))
		if *instance == "" {
			id, err := gate.CurrentIdentity(root)
			must(err)
			*instance = id.Instance
		}
		s, err := gate.ReleaseSession(root, *instance, *base, *force)
		must(err)
		fmt.Printf("released %s (%s)\n", s.Identity, s.Worktree)
	default:
		usage()
	}
}

func specCmd(root, sub string, args []string) {
	s, err := gate.Load(root)
	must(err)
	switch sub {
	case "lint":
		errs := gate.Lint(s, time.Now())
		_, terrs := gate.Trace(s)
		report("spec lint", append(errs, terrs...))
	case "trace":
		fs := flag.NewFlagSet("trace", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		must(fs.Parse(args))
		rows, errs := gate.Trace(s)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			must(enc.Encode(rows))
		} else {
			for _, r := range rows {
				fmt.Printf("%-14s active=%v planned=%v\n", r.Req, r.Scenarios, r.Planned)
			}
		}
		report("spec trace", errs)
	case "lock":
		l, err := gate.ReadLedger(root)
		must(err)
		report("spec lock", gate.CheckLock(s, l))
	case "approve":
		fs := flag.NewFlagSet("approve", flag.ExitOnError)
		ids := fs.String("ids", "", "comma-separated scenario ids")
		unapproved := fs.Bool("unapproved", false, "approve every scenario whose text is not yet approved")
		tier := fs.String("tier", "", "P0..P3 (raised automatically to the rule minimum)")
		author := fs.String("author", "", "identity of the spec author")
		reviewer := fs.String("reviewer", "", "identity of the reviewer (default: this session, see `gate agent whoami`)")
		note := fs.String("note", "", "why this tier (required for P0 and P1)")
		ref := fs.String("ref", "", "optional link to the review discussion")
		must(fs.Parse(args))
		if *reviewer == "" {
			id, err := gate.CurrentIdentity(root)
			must(err)
			*reviewer = id.String()
		}
		l, err := gate.ReadLedger(root)
		must(err)
		list := splitList(*ids)
		if *unapproved {
			list = append(list, "*unapproved")
		}
		done, err := gate.Approve(s, l, gate.ApproveRequest{IDs: list, Tier: *tier, Author: *author, Reviewer: *reviewer,
			Note: *note, Ref: *ref, Date: time.Now().UTC().Format("2006-01-02")})
		must(err)
		must(l.Write(root))
		fmt.Printf("approved: %v\n", done)
	case "human":
		fs := flag.NewFlagSet("human", flag.ExitOnError)
		id := fs.String("id", "", "scenario id")
		status := fs.String("status", "", "accepted|rejected")
		by := fs.String("by", "epzero", "reviewer GitHub login (defaults to epzero)")
		at := fs.String("at", time.Now().UTC().Format("2006-01-02"), "decision date (YYYY-MM-DD)")
		note := fs.String("note", "", "optional note")
		must(fs.Parse(args))
		l, err := gate.ReadLedger(root)
		must(err)
		e, _ := l.Entry(*id)
		person, perr := gate.ParseIdentity("human:" + strings.TrimPrefix(*by, "human:"))
		if e == nil || (*status != "accepted" && *status != "rejected") || perr != nil {
			fmt.Fprintln(os.Stderr, "human: need an existing -id, -status accepted|rejected and valid -by <github-login>")
			os.Exit(2)
		}
		e.Human = gate.Human{Status: *status, By: person.String(), At: *at, Note: *note}
		must(l.Write(root))
	case "queue":
		l, err := gate.ReadLedger(root)
		must(err)
		for _, line := range gate.Queue(l) {
			fmt.Println(line)
		}
	default:
		usage()
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func report(name string, errs []string) {
	if len(errs) == 0 {
		fmt.Printf("%s: ok\n", name)
		return
	}
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "%s: %s\n", name, e)
	}
	fmt.Fprintf(os.Stderr, "%s: %d problem(s)\n", name, len(errs))
	os.Exit(1)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gate agent new|whoami|list|status|release | wp | review plan [-base <ref>] [-head <ref>] | docs [-write] | layering | gomod | module-roots | spec lint|lock|trace|approve|human|queue | release-check | bdd-skips | redgreen -base <ref>")
	os.Exit(2)
}
