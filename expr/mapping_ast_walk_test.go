package expr

import (
	"reflect"
	"testing"

	"github.com/itchyny/gojq"
)

func collectGojqASTTypes(root reflect.Type, seen map[string]bool) []reflect.Type {
	if root == nil {
		return nil
	}
	for root.Kind() == reflect.Pointer {
		root = root.Elem()
	}
	if root.PkgPath() != "github.com/itchyny/gojq" || root.Kind() != reflect.Struct {
		return nil
	}
	if seen[root.Name()] {
		return nil
	}
	seen[root.Name()] = true
	out := []reflect.Type{root}
	for i := 0; i < root.NumField(); i++ {
		ft := root.Field(i).Type
		for ft.Kind() == reflect.Slice || ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.PkgPath() == "github.com/itchyny/gojq" && ft.Kind() == reflect.Struct {
			out = append(out, collectGojqASTTypes(ft, seen)...)
		}
	}
	return out
}

func TestASTWalkerCoversGojqStructTypes(t *testing.T) {
	types := collectGojqASTTypes(reflect.TypeOf(gojq.Query{}), map[string]bool{})
	for _, typ := range types {
		if !gojqASTStructs[typ.Name()] {
			t.Fatalf("gojqASTStructs missing %q", typ.Name())
		}
		ptr := reflect.New(typ)
		if _, _, err := walkAST(ptr); err != nil {
			t.Fatalf("walk %s: %v", typ.Name(), err)
		}
	}
}

func TestASTWalkerRejectsUnknownTermType(t *testing.T) {
	term := &gojq.Term{Type: gojq.TermType(9999)}
	q := &gojq.Query{Term: term}
	if n, ok := scanBannedAST(q); !ok || n == "" {
		t.Fatalf("want blocked, got %q ok=%v", n, ok)
	}
}
