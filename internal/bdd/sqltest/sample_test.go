package sqltest_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/sqltest"
	psql "github.com/epzero/basamento/platform/sql"
)

func TestListHoldingItem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	mig, err := sqltest.MigrationsThrough(3, false)
	if err != nil {
		t.Fatal(err)
	}
	s, err := psql.Open(context.Background(), "sqlite:///"+path, mig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	repo := sqltest.NewSampleRepo(s)
	ctx := core.WithTenant(context.Background(), "acme")
	if err := repo.StoreItem(ctx, "k1", sqltest.StoreOptions{ListName: "groceries"}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreItemViolatingConstraintRules(t *testing.T) {
	dir := t.TempDir()
	mig, err := sqltest.MigrationsThrough(3, false)
	if err != nil {
		t.Fatal(err)
	}
	s, err := psql.Open(context.Background(), "sqlite:///"+filepath.Join(dir, "kit.db"), mig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	repo := sqltest.NewSampleRepo(s)
	ctx := core.WithTenant(context.Background(), "acme")

	for _, rule := range []string{"not-null", "check"} {
		err := repo.StoreItemViolatingConstraint(ctx, "k-"+rule, rule)
		if got := core.CodeOf(err); got != core.CodeInvalidArgument {
			t.Fatalf("%s: got %v, want invalid_argument", rule, err)
		}
	}
	if err := repo.StoreItemViolatingConstraint(ctx, "k-other", "primary-key"); core.CodeOf(err) == core.CodeInvalidArgument {
		t.Fatalf("unknown rule passed as invalid_argument: %v", err)
	}
}

func TestConcurrentOpen(t *testing.T) {
	dir := t.TempDir()
	raw := "sqlite:///" + filepath.Join(dir, "kit.db")
	mig, err := sqltest.MigrationsThrough(3, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, e := psql.Open(ctx, raw, mig)
			if s != nil {
				_ = s.Close()
			}
			errs[i] = e
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("open %d: %v", i, e)
		}
	}
}

func TestStoreItemRejectsNULInKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	mig, _ := sqltest.MigrationsThrough(3, false)
	s, err := psql.Open(context.Background(), "sqlite:///"+path, mig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	repo := sqltest.NewSampleRepo(s)
	ctx := core.WithTenant(context.Background(), "acme")
	if err := repo.StoreItem(ctx, "k\x00", sqltest.StoreOptions{}); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v", err)
	}
}

func TestConcurrentOpenWhileHeld(t *testing.T) {
	dir := t.TempDir()
	raw := "sqlite:///" + filepath.Join(dir, "kit.db")
	mig, _ := sqltest.MigrationsThrough(3, false)
	ctx := context.Background()
	s1, err := psql.Open(ctx, raw, mig)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s, e := psql.Open(ctx, raw, mig)
		if s != nil {
			_ = s.Close()
		}
		errs[0] = e
	}()
	wg.Wait()
	_ = s1.Close()
	if errs[0] != nil {
		t.Fatalf("second: %+v", errs[0])
	}
}
