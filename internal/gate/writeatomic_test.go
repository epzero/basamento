package gate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDirSyncErr(t *testing.T) {
	var boom = errors.New("boom")
	pathErr := func(errno syscall.Errno) error {
		return &fs.PathError{Op: "sync", Path: "/d", Err: errno}
	}
	for _, tc := range []struct {
		name string
		in   error
		want error
	}{
		{"nil", nil, nil},
		{"EINVAL", syscall.EINVAL, nil},
		{"ErrUnsupported", errors.ErrUnsupported, nil},
		{"EIO", syscall.EIO, syscall.EIO},
		{"wrapped EINVAL", pathErr(syscall.EINVAL), nil},
		{"wrapped EIO", pathErr(syscall.EIO), syscall.EIO},
		{"other", boom, boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dirSyncErr(tc.in)
			if !sameDirSyncResult(got, tc.want) {
				t.Fatalf("dirSyncErr(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func sameDirSyncResult(got, want error) bool {
	if got == nil && want == nil {
		return true
	}
	if got == nil || want == nil {
		return false
	}
	return errors.Is(got, want)
}

func TestWriteFileAtomicDirSync(t *testing.T) {
	checkContents := func(t *testing.T, target string, want []byte) {
		t.Helper()
		b, err := os.ReadFile(target)
		if err != nil || string(b) != string(want) {
			t.Fatalf("file = %q, %v; want %q", b, err, want)
		}
	}
	t.Run("unsupported dir sync is ignored", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "hand-off.json")
		want := []byte("payload\n")
		err := writeFileAtomicWith(target, want, func(string) error {
			return &fs.PathError{Op: "sync", Path: dir, Err: syscall.EINVAL}
		})
		if err != nil {
			t.Fatalf("EINVAL after rename: %v", err)
		}
		checkContents(t, target, want)
	})
	t.Run("EIO from dir sync is returned", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "hand-off.json")
		want := []byte("other\n")
		err := writeFileAtomicWith(target, want, func(string) error {
			return fmt.Errorf("sync: %w", syscall.EIO)
		})
		if !errors.Is(err, syscall.EIO) {
			t.Fatalf("want EIO, got %v", err)
		}
		checkContents(t, target, want)
	})
	t.Run("ok sync", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "hand-off.json")
		want := []byte("ok\n")
		if err := writeFileAtomicWith(target, want, syncDirDefault); err != nil {
			t.Fatal(err)
		}
		checkContents(t, target, want)
	})
}
