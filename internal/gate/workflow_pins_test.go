package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const checkoutPin = "11d5960a326750d5838078e36cf38b85af677262"

// minWorkflowFiles is the lower bound for TestWorkflowActionPinsOnRepo so an empty directory cannot pass.
const minWorkflowFiles = 3

// minWorkflowUses is the lower bound for TestWorkflowActionPinsOnRepo so a vacuous zero-uses scan cannot pass.
const minWorkflowUses = 1

const unpinnedWorkflowFixture = `name: old
on: push
jobs:
  gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - uses: actions/cache@v4
      - uses: actions/upload-artifact@v4
`

var workflowFullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
var workflowVersionTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
var workflowDockerSHA = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// workflowPinResult holds pin violations plus how many workflow files and uses: entries were examined.
type workflowPinResult struct {
	Violations []string
	FileCount  int
	UsesCount  int
}

// releaseCommentFirstToken takes the first word after # on a uses line; further words on the comment line are allowed.
func releaseCommentFirstToken(lineComment string) string {
	lineComment = strings.TrimSpace(lineComment)
	lineComment = strings.TrimPrefix(lineComment, "#")
	lineComment = strings.TrimSpace(lineComment)
	if lineComment == "" {
		return ""
	}
	return strings.Fields(lineComment)[0]
}

// checkWorkflowActionPin enforces remote actions at a full SHA with a vMAJOR.MINOR.PATCH comment; ./ and docker@sha256 are exempt.
func checkWorkflowActionPin(uses, comment string) string {
	uses = strings.TrimSpace(uses)
	if uses == "" {
		return "empty uses"
	}
	if strings.HasPrefix(uses, "./") {
		return ""
	}
	if strings.HasPrefix(uses, "docker://") {
		if workflowDockerSHA.MatchString(uses) {
			return ""
		}
		return fmt.Sprintf("docker action %q must pin image with @sha256:<64-hex>", uses)
	}
	at := strings.LastIndex(uses, "@")
	if at < 0 {
		return fmt.Sprintf("remote action %q must pin to a full commit SHA", uses)
	}
	ref := uses[at+1:]
	if !workflowFullSHA.MatchString(ref) {
		return fmt.Sprintf("remote action %q must use a 40-character commit SHA, not %q", uses, ref)
	}
	if comment == "" {
		return fmt.Sprintf("remote action %q needs a trailing comment naming the release (e.g. # v1.2.3)", uses)
	}
	if !workflowVersionTag.MatchString(comment) {
		return fmt.Sprintf("release comment for %q must look like vMAJOR.MINOR.PATCH, got %q", uses, comment)
	}
	return ""
}

// visitWorkflowYAML walks parsed YAML mappings for uses keys so quoting, flow style and multi-word comments cannot hide an entry; run: text is a scalar, not a uses key.
func visitWorkflowYAML(n *yaml.Node, file string, res *workflowPinResult) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode, yaml.AliasNode:
		for _, c := range n.Content {
			visitWorkflowYAML(c, file, res)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if key.Kind == yaml.ScalarNode && key.Value == "uses" && val.Kind == yaml.ScalarNode {
				res.UsesCount++
				comment := releaseCommentFirstToken(val.LineComment)
				if msg := checkWorkflowActionPin(val.Value, comment); msg != "" {
					res.Violations = append(res.Violations, fmt.Sprintf("%s:%d: %s", file, val.Line, msg))
				}
			}
			visitWorkflowYAML(val, file, res)
		}
	default:
		for _, c := range n.Content {
			visitWorkflowYAML(c, file, res)
		}
	}
}

// checkWorkflowActionPins reads every *.yml and *.yaml in workflowsDir; a missing directory yields zero counts, so callers must assert minimums.
func checkWorkflowActionPins(workflowsDir string) (workflowPinResult, error) {
	var out workflowPinResult
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		path := filepath.Join(workflowsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return out, err
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return out, fmt.Errorf("%s: %w", path, err)
		}
		out.FileCount++
		visitWorkflowYAML(&doc, path, &out)
	}
	return out, nil
}

// writeWorkflowFixture writes one workflow file into a temp directory for table-driven pin tests.
func writeWorkflowFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowYAMLLineCommentOnUses(t *testing.T) {
	const body = "jobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@" + checkoutPin + " # v4.4.0\n"
	dir := t.TempDir()
	writeWorkflowFixture(t, dir, "one.yml", body)
	res, err := checkWorkflowActionPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.UsesCount != 1 {
		t.Fatalf("uses count: %d", res.UsesCount)
	}
	if len(res.Violations) > 0 {
		t.Fatalf("unexpected violations: %v", res.Violations)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	var found *yaml.Node
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == "uses" && n.Content[i+1].Kind == yaml.ScalarNode {
					found = n.Content[i+1]
				}
				walk(n.Content[i+1])
			}
		} else {
			for _, c := range n.Content {
				walk(c)
			}
		}
	}
	walk(&doc)
	if found == nil {
		t.Fatal("uses scalar not found")
	}
	if found.LineComment != "# v4.4.0" {
		t.Fatalf("LineComment on value node: got %q", found.LineComment)
	}
}

func TestCheckWorkflowActionPin(t *testing.T) {
	cases := []struct {
		name    string
		uses    string
		comment string
		ok      bool
	}{
		{"pinned with comment", "actions/checkout@" + checkoutPin, "v4.4.0", true},
		{"tag only", "actions/checkout@v4", "", false},
		{"branch ref", "actions/checkout@my-branch", "", false},
		{"short sha", "actions/checkout@" + checkoutPin[:7], "v4.4.0", false},
		{"sha without comment", "actions/checkout@" + checkoutPin, "", false},
		{"local action", "./.github/actions/foo", "", true},
		{"docker digest", "docker://postgres:16-bookworm@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "", true},
		{"docker tag", "docker://postgres:16-bookworm", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := checkWorkflowActionPin(tc.uses, tc.comment)
			if tc.ok && msg != "" {
				t.Fatalf("want ok, got %q", msg)
			}
			if !tc.ok && msg == "" {
				t.Fatal("want violation, got ok")
			}
		})
	}
}

func TestWorkflowActionPinFixtures(t *testing.T) {
	const pinnedStep = "      - uses: actions/checkout@" + checkoutPin + " # v4.4.0\n"
	cases := []struct {
		name      string
		file      string
		body      string
		wantViol  int
		wantOk    bool
		wantErr   bool
		errSubstr string
	}{
		{
			name:     "unpinned tag",
			file:     "tag.yml",
			body:     "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n",
			wantViol: 1,
		},
		{
			name:     "branch ref",
			file:     "branch.yml",
			body:     "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@feature-x\n",
			wantViol: 1,
		},
		{
			name:     "short sha",
			file:     "short.yml",
			body:     fmt.Sprintf("on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@%s # v4.4.0\n", checkoutPin[:7]),
			wantViol: 1,
		},
		{
			name:     "sha without comment",
			file:     "nocomment.yml",
			body:     fmt.Sprintf("on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@%s\n", checkoutPin),
			wantViol: 1,
		},
		{
			name:     "bad release comment",
			file:     "badcomment.yml",
			body:     fmt.Sprintf("on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@%s # latest\n", checkoutPin),
			wantViol: 1,
		},
		{
			name:     "multi word comment ok",
			file:     "longcomment.yml",
			body:     "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@" + checkoutPin + " # v4.4.0 (security fix)\n",
			wantOk:   true,
			wantViol: 0,
		},
		{
			name:     "quoted pinned",
			file:     "quoted.yml",
			body:     "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: \"actions/checkout@" + checkoutPin + "\" # v4.4.0\n",
			wantOk:   true,
			wantViol: 0,
		},
		{
			name:     "unpinned multiword trailing comment",
			file:     "regexphole.yml",
			body:     "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4 # keep this one\n",
			wantViol: 1,
		},
		{
			name:     "job level reusable unpinned",
			file:     "reusable-bad.yml",
			body:     "on: push\njobs:\n  call:\n    uses: octo-org/example/.github/workflows/ci.yml@v1\n",
			wantViol: 1,
		},
		{
			name:     "job level reusable pinned",
			file:     "reusable-good.yml",
			body:     "on: push\njobs:\n  call:\n    uses: octo-org/example/.github/workflows/ci.yml@" + checkoutPin + " # v1.0.0\n",
			wantOk:   true,
			wantViol: 0,
		},
		{
			name:     "local action",
			file:     "local.yml",
			body:     "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: ./.github/actions/foo\n",
			wantOk:   true,
			wantViol: 0,
		},
		{
			name:     "docker digest",
			file:     "docker-ok.yml",
			body:     "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    container:\n      image: docker://postgres:16-bookworm@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n    steps:\n      - uses: " + `"docker://alpine:3@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"` + "\n",
			wantOk:   true,
			wantViol: 0,
		},
		{
			name:     "docker tag",
			file:     "docker-bad.yml",
			body:     "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: docker://postgres:16-bookworm\n",
			wantViol: 1,
		},
		{
			name:     "yaml extension",
			file:     "pins.yaml",
			body:     "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/setup-go@v5\n",
			wantViol: 1,
		},
		{
			name:     "run block not flagged",
			file:     "runblock.yml",
			body:     "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          echo uses: foo@v1\n" + pinnedStep,
			wantOk:   true,
			wantViol: 0,
		},
		{
			name:      "invalid yaml",
			file:      "broken.yml",
			body:      "on: push\njobs:\n  j: [\n",
			wantErr:   true,
			errSubstr: "broken.yml",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeWorkflowFixture(t, dir, tc.file, tc.body)
			res, err := checkWorkflowActionPins(dir)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("want error containing %q, got %v", tc.errSubstr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantOk && len(res.Violations) > 0 {
				t.Fatalf("violations: %v", res.Violations)
			}
			if tc.wantViol > 0 && len(res.Violations) != tc.wantViol {
				t.Fatalf("want %d violations, got %d: %v", tc.wantViol, len(res.Violations), res.Violations)
			}
		})
	}
}

func TestWorkflowActionPinsRejectUnpinnedFixture(t *testing.T) {
	dir := t.TempDir()
	writeWorkflowFixture(t, dir, "old.yml", unpinnedWorkflowFixture)
	res, err := checkWorkflowActionPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) != 4 {
		t.Fatalf("want 4 violations (one per uses), got %d:\n%s", len(res.Violations), strings.Join(res.Violations, "\n"))
	}
}

func TestWorkflowActionPinsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	res, err := checkWorkflowActionPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.FileCount != 0 || res.UsesCount != 0 {
		t.Fatalf("empty dir: files=%d uses=%d", res.FileCount, res.UsesCount)
	}
	if len(res.Violations) != 0 {
		t.Fatal("unexpected violations")
	}
}

func TestWorkflowActionPinsOnRepo(t *testing.T) {
	dir := filepath.Join(makefileRoot(t), ".github", "workflows")
	res, err := checkWorkflowActionPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.FileCount < minWorkflowFiles {
		t.Fatalf("workflow files: got %d want at least %d", res.FileCount, minWorkflowFiles)
	}
	if res.UsesCount < minWorkflowUses {
		t.Fatalf("uses entries: got %d want at least %d", res.UsesCount, minWorkflowUses)
	}
	if len(res.Violations) > 0 {
		t.Fatalf("workflow pin violations:\n%s", strings.Join(res.Violations, "\n"))
	}
}
