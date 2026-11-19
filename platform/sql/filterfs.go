package sql

import (
	"fmt"
	"io/fs"
	"path"
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

func (f filterFS) migrationAllowed(name string) bool {
	base := path.Base(name)
	if !strings.HasSuffix(base, ".sql") {
		return true
	}
	if i := strings.Index(base, "_"); i > 0 {
		v, err := strconv.Atoi(base[:i])
		if err == nil {
			return v <= f.max
		}
	}
	return true
}

func (f filterFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if !f.migrationAllowed(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return f.all.Open(name)
}

func (f filterFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
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
		full := path.Join(name, e.Name())
		if !f.migrationAllowed(full) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (f filterFS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrInvalid}
	}
	if !f.migrationAllowed(name) {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrNotExist}
	}
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

func (f versionExcludeFS) migrationAllowed(name string) bool {
	base := path.Base(name)
	prefix := fmt.Sprintf("%05d_", f.exclude)
	if strings.HasPrefix(base, prefix) && strings.HasSuffix(base, ".sql") {
		return false
	}
	return true
}

func (f versionExcludeFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if !f.migrationAllowed(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return f.all.Open(name)
}

func (f versionExcludeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
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
		full := path.Join(name, e.Name())
		if !f.migrationAllowed(full) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (f versionExcludeFS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrInvalid}
	}
	if !f.migrationAllowed(name) {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrNotExist}
	}
	return fs.ReadFile(f.all, name)
}
