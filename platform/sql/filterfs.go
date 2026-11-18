package sql

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// FilterMigrations returns an fs.FS containing only migrations with version <= maxVer.
func FilterMigrations(all fs.FS, maxVer int) (fs.FS, error) {
	if maxVer <= 0 {
		return all, nil
	}
	return filterFS{all: all, max: maxVer}, nil
}

type filterFS struct {
	all fs.FS
	max int
}

func (f filterFS) Open(name string) (fs.File, error) {
	return f.all.Open(name)
}

func (f filterFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(f.all, name)
	if err != nil {
		return nil, err
	}
	var out []fs.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e)
			continue
		}
		if !strings.HasSuffix(e.Name(), ".sql") {
			out = append(out, e)
			continue
		}
		base := e.Name()
		if i := strings.Index(base, "_"); i > 0 {
			v, err := strconv.Atoi(base[:i])
			if err == nil && v <= f.max {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func (f filterFS) ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(f.all, name)
}

// ExcludeMigrationVersion drops one migration version from the set.
func ExcludeMigrationVersion(all fs.FS, ver int) (fs.FS, error) {
	return versionExcludeFS{all: all, exclude: ver}, nil
}

type versionExcludeFS struct {
	all     fs.FS
	exclude int
}

func (f versionExcludeFS) Open(name string) (fs.File, error) { return f.all.Open(name) }

func (f versionExcludeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(f.all, name)
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("%05d_", f.exclude)
	var out []fs.DirEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f versionExcludeFS) ReadFile(name string) ([]byte, error) { return fs.ReadFile(f.all, name) }
