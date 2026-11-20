package gate

import (
	"reflect"
	"testing"
)

func TestLayeringExcludedFixtureGOOSIndependent(t *testing.T) {
	files := map[string]string{
		"low/a/active.go": "package a\n",
		"high/b/b.go":     "package b\n",
		"low/a/other.go": layeringExcludedGoFile(`package a

import _ "example.com/mod/high/b"
`),
	}
	want := []string{"low/a (layer low, in an excluded file other.go) may not import high/b (layer high)"}

	var ref []string
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			t.Setenv("GOOS", goos)
			root := layeringModule(t, files)
			got, err := Layering(root)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("GOOS=%s: got %v, want %v", goos, got, want)
			}
			if ref == nil {
				ref = got
			} else if !reflect.DeepEqual(got, ref) {
				t.Fatalf("GOOS=%s output differs from %s: %v vs %v", goos, "first", got, ref)
			}
		})
	}
}
