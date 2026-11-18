package gate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// goDirectiveLine matches a go directive only in the form "go 1.M.P" with no trailing text.
var goDirectiveLine = regexp.MustCompile(`^go ([0-9]+)\.([0-9]+)\.([0-9]+)$`)

// toolchainDirective matches a toolchain line with optional leading indentation (ADR-0014).
var toolchainDirective = regexp.MustCompile(`^[ \t]*toolchain[ \t]`)

// GoModVersion is a parsed "go 1.M.P" directive.
type GoModVersion struct {
	Major, Minor, Patch int
}

// String returns the directive line form.
func (v GoModVersion) String() string {
	return fmt.Sprintf("go %d.%d.%d", v.Major, v.Minor, v.Patch)
}

type parsedGoMod struct {
	ver           GoModVersion
	withoutGoLine []byte
}

func parseGoModFile(content []byte) (parsedGoMod, error) {
	var (
		ver   GoModVersion
		found bool
		out   bytes.Buffer
	)
	lines := bytes.Split(content, []byte("\n"))
	for i, line := range lines {
		raw := bytes.TrimSuffix(line, []byte("\r"))
		if toolchainDirective.Match(raw) {
			return parsedGoMod{}, fmt.Errorf("go.mod: toolchain directive present")
		}
		if m := goDirectiveLine.FindSubmatch(raw); m != nil {
			if found {
				return parsedGoMod{}, fmt.Errorf("go.mod: multiple go directives")
			}
			found = true
			var err error
			ver.Major, err = strconv.Atoi(string(m[1]))
			if err != nil {
				return parsedGoMod{}, fmt.Errorf("go.mod: %w", err)
			}
			ver.Minor, err = strconv.Atoi(string(m[2]))
			if err != nil {
				return parsedGoMod{}, fmt.Errorf("go.mod: %w", err)
			}
			ver.Patch, err = strconv.Atoi(string(m[3]))
			if err != nil {
				return parsedGoMod{}, fmt.Errorf("go.mod: %w", err)
			}
			continue
		}
		if i > 0 {
			out.WriteByte('\n')
		}
		out.Write(line)
	}
	if !found {
		return parsedGoMod{}, fmt.Errorf("go.mod: no go 1.M.P directive")
	}
	return parsedGoMod{ver: ver, withoutGoLine: out.Bytes()}, nil
}

// ParseGoModDirective finds the single "go 1.M.P" line in content and rejects
// toolchain lines and any other go directive form (ADR-0014).
func ParseGoModDirective(content []byte) (GoModVersion, error) {
	p, err := parseGoModFile(content)
	if err != nil {
		return GoModVersion{}, err
	}
	return p.ver, nil
}

// PatchOnlyGoModBump reports whether head is a medium-tier patch-only bump of
// base: both parse, share the same 1.M release line, head patch is greater,
// and the files are byte-identical apart from that go line.
func PatchOnlyGoModBump(base, head []byte) bool {
	baseParsed, err := parseGoModFile(base)
	if err != nil {
		return false
	}
	headParsed, err := parseGoModFile(head)
	if err != nil {
		return false
	}
	if !bytes.Equal(baseParsed.withoutGoLine, headParsed.withoutGoLine) {
		return false
	}
	if baseParsed.ver.Major != headParsed.ver.Major || baseParsed.ver.Minor != headParsed.ver.Minor {
		return false
	}
	return headParsed.ver.Patch > baseParsed.ver.Patch
}

// CheckSameGoModLine returns errors when tracked go.mod files disagree on the
// go directive or any line does not parse (ADR-0014).
func CheckSameGoModLine(root string) ([]string, error) {
	out, err := git(root, "ls-files")
	if err != nil {
		return nil, err
	}
	var mods []string
	for _, f := range strings.Split(out, "\n") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if f == "go.mod" || strings.HasSuffix(f, "/go.mod") {
			mods = append(mods, f)
		}
	}
	if len(mods) == 0 {
		return nil, nil
	}
	var (
		want  GoModVersion
		ref   string
		first bool
		errs  []string
	)
	for _, f := range mods {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			if os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("%s: tracked but missing", f))
				continue
			}
			return nil, err
		}
		ver, err := ParseGoModDirective(b)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		if !first {
			want = ver
			ref = f
			first = true
			continue
		}
		if ver != want {
			errs = append(errs, fmt.Sprintf("%s: go directive %s differs from %s (%s)", f, ver, ref, want))
		}
	}
	return errs, nil
}

func modContentAtRef(root, ref, file string) ([]byte, bool, error) {
	if ref == "" {
		b, err := os.ReadFile(filepath.Join(root, file))
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return b, true, err
	}
	if _, err := git(root, "cat-file", "-e", ref+":"+file); err != nil {
		return nil, false, nil
	}
	b, err := gitBytes(root, "show", ref+":"+file)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func gitBytes(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
