package blob

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func countSuffixFiles(root, suffix string) (int, error) {
	var n int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), suffix) {
			n++
		}
		return nil
	})
	return n, err
}

func dirEntryCount(root string) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

// sniffLen matches gocloud.dev/blob's content-type sniff buffer; stage writers
// omit ContentType, so the underlying fileblob writer opens only after this much
// body data is buffered.
const gocloudSniffLen = 512

type gateReader struct {
	data  []byte
	step  int
	armed chan struct{}
	wait  chan struct{}
}

func (g *gateReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	switch g.step {
	case 0:
		if len(g.data) == 0 {
			return 0, io.EOF
		}
		g.step++
		return copy(p, g.data[:gocloudSniffLen]), nil
	case 1:
		close(g.armed)
		<-g.wait
		g.step++
		n := copy(p, g.data[gocloudSniffLen:])
		if n < len(g.data)-1 {
			return n, nil
		}
		return n, io.EOF
	default:
		return 0, io.EOF
	}
}

func TestFilePutStagesTempNextToObjectNotTMPDIR(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)
	dataDir := t.TempDir()
	raw := (&url.URL{Scheme: "file", Path: dataDir}).String()
	st, _, err := Open(context.Background(), raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	body := make([]byte, gocloudSniffLen+64)
	for i := range body {
		body[i] = byte(i)
	}
	key := ContentKeyOf(body)
	ctx := core.WithTenant(context.Background(), "acme")
	objPath, err := objectPath("acme", key)
	if err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(dataDir, filepath.FromSlash(objPath))

	gr := &gateReader{
		data:  body,
		armed: make(chan struct{}),
		wait:  make(chan struct{}),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- st.Put(ctx, key, gr, "text/plain")
	}()

	<-gr.armed

	if n, err := dirEntryCount(tmpRoot); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("TMPDIR: want empty, got %d entries", n)
	}
	tmpN, err := countSuffixFiles(dataDir, ".tmp")
	if err != nil {
		t.Fatal(err)
	}
	tmpRootN, _ := countSuffixFiles(tmpRoot, ".tmp")
	if tmpN != 1 {
		t.Fatalf("data dir: want 1 .tmp file, got %d (tmpdir .tmp count %d)", tmpN, tmpRootN)
	}
	if _, err := os.Stat(finalPath); err == nil {
		t.Fatal("final object should not exist yet")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat final: %v", err)
	}

	close(gr.wait)
	if err := <-errCh; err != nil {
		t.Fatalf("Put: %v", err)
	}
	tmpN, err = countSuffixFiles(dataDir, ".tmp")
	if err != nil {
		t.Fatal(err)
	}
	if tmpN != 0 {
		t.Fatalf("after Put: want no .tmp under data dir, got %d", tmpN)
	}
	if n, err := dirEntryCount(tmpRoot); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("after Put: TMPDIR still empty, got %d entries", n)
	}
	rc, err := st.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("read back: %q", got)
	}
}

func TestFilePutFailureLeavesNoTmp(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)
	dataDir := t.TempDir()
	raw := (&url.URL{Scheme: "file", Path: dataDir}).String()
	st, _, err := Open(context.Background(), raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("expected"))

	if err := st.Put(ctx, key, strings.NewReader("mismatch"), "text/plain"); err == nil {
		t.Fatal("want hash mismatch error")
	}
	if n, err := countSuffixFiles(dataDir, ".tmp"); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("hash mismatch: want no .tmp, got %d", n)
	}

	if err := st.Put(ctx, key, &failReader{n: 0}, "text/plain"); err == nil {
		t.Fatal("want read error")
	}
	if n, err := countSuffixFiles(dataDir, ".tmp"); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("read fail: want no .tmp, got %d", n)
	}
}

func TestEnsureFileBlobNoTmpDirQuery(t *testing.T) {
	t.Run("adds when missing", func(t *testing.T) {
		u, err := url.Parse("file:///var/data?metadata=skip")
		if err != nil {
			t.Fatal(err)
		}
		ensureFileBlobNoTmpDir(u)
		q := u.Query()
		if q.Get("metadata") != "skip" {
			t.Fatalf("metadata param lost: %v", q)
		}
		if q.Get("no_tmp_dir") == "" {
			t.Fatal("want no_tmp_dir added")
		}
	})
	t.Run("does not override", func(t *testing.T) {
		u, err := url.Parse("file:///var/data?no_tmp_dir=1")
		if err != nil {
			t.Fatal(err)
		}
		before := u.RawQuery
		ensureFileBlobNoTmpDir(u)
		if u.RawQuery != before {
			t.Fatalf("query changed: %q -> %q", before, u.RawQuery)
		}
	})
}

func TestFileOpenPreservesNoTmpDirParam(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)
	dataDir := t.TempDir()
	raw := fmt.Sprintf("file://%s?no_tmp_dir=1", dataDir)
	st, _, err := Open(context.Background(), raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("ok"))
	if err := st.Put(ctx, key, strings.NewReader("ok"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if n, err := countSuffixFiles(tmpRoot, ".tmp"); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("TMPDIR should stay empty with explicit no_tmp_dir, got %d .tmp", n)
	}
}

func TestFileOpenMetadataSkipParam(t *testing.T) {
	dataDir := t.TempDir()
	raw := (&url.URL{Scheme: "file", Path: dataDir, RawQuery: "metadata=skip"}).String()
	st, _, err := Open(context.Background(), raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("meta"))
	if err := st.Put(ctx, key, strings.NewReader("meta"), "text/plain"); err != nil {
		t.Fatal(err)
	}
}
