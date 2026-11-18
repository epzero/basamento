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

var toolchainLine = regexp.MustCompile(`(?m)^toolchain `)

// GoModVersion is a parsed "go 1.M.P" directive.
type GoModVersion struct {
	Major, Minor, Patch int
}

// String returns the directive line form.
func (v GoModVersion) String() string {
	return fmt.Sprintf("go %d.%d.%d", v.Major, v.Minor, v.Patch)
}

// ReleaseLine is "1.M" from the version.
func (v GoModVersion) ReleaseLine() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// ParseGoModDirective finds the single "go 1.M.P" line in content and rejects
// toolchain lines and any other go directive form (ADR-0014).
func ParseGoModDirective(content []byte) (GoModVersion, error) {
	if toolchainLine.Match(content) {
		return GoModVersion{}, fmt.Errorf("go.mod: toolchain directive present")
	}
	var (
		ver   GoModVersion
		found bool
	)
	for _, line := range bytes.Split(content, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		m := goDirectiveLine.FindSubmatch(line)
		if m == nil {
			continue
		}
		if found {
			return GoModVersion{}, fmt.Errorf("go.mod: multiple go directives")
		}
		found = true
		var err error
		ver.Major, err = strconv.Atoi(string(m[1]))
		if err != nil {
			return GoModVersion{}, fmt.Errorf("go.mod: %w", err)
		}
		ver.Minor, err = strconv.Atoi(string(m[2]))
		if err != nil {
			return GoModVersion{}, fmt.Errorf("go.mod: %w", err)
		}
		ver.Patch, err = strconv.Atoi(string(m[3]))
		if err != nil {
			return GoModVersion{}, fmt.Errorf("go.mod: %w", err)
		}
	}
	if !found {
		return GoModVersion{}, fmt.Errorf("go.mod: no go 1.M.P directive")
	}
	return ver, nil
}

// PatchOnlyGoModBump reports whether head is a medium-tier patch-only bump of
// base: both parse, share the same 1.M release line, head patch is greater,
// and the files are byte-identical apart from that go line.
func PatchOnlyGoModBump(base, head []byte) bool {
	baseRest, baseVer, err := goModWithoutDirective(base)
	if err != nil {
		return false
	}
	headRest, headVer, err := goModWithoutDirective(head)
	if err != nil {
		return false
	}
	if !bytes.Equal(baseRest, headRest) {
		return false
	}
	if baseVer.Major != headVer.Major || baseVer.Minor != headVer.Minor {
		return false
	}
	return headVer.Patch > baseVer.Patch
}

func goModWithoutDirective(content []byte) ([]byte, GoModVersion, error) {
	if toolchainLine.Match(content) {
		return nil, GoModVersion{}, fmt.Errorf("toolchain")
	}
	var (
		ver   GoModVersion
		found bool
		out   bytes.Buffer
	)
	lines := bytes.Split(content, []byte("\n"))
	for i, line := range lines {
		raw := bytes.TrimSuffix(line, []byte("\r"))
		if m := goDirectiveLine.FindSubmatch(raw); m != nil {
			if found {
				return nil, GoModVersion{}, fmt.Errorf("multiple go directives")
			}
			found = true
			var err error
			ver.Major, err = strconv.Atoi(string(m[1]))
			if err != nil {
				return nil, GoModVersion{}, err
			}
			ver.Minor, err = strconv.Atoi(string(m[2]))
			if err != nil {
				return nil, GoModVersion{}, err
			}
			ver.Patch, err = strconv.Atoi(string(m[3]))
			if err != nil {
				return nil, GoModVersion{}, err
			}
			continue
		}
		if i > 0 {
			out.WriteByte('\n')
		}
		out.Write(line)
	}
	if !found {
		return nil, GoModVersion{}, fmt.Errorf("no go directive")
	}
	return out.Bytes(), ver, nil
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
	b, err := gitBytes(root, "show", ref+":"+file)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
			return nil, false, nil
		}
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
