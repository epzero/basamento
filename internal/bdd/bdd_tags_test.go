package bdd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/cucumber/godog"
)

const tagProbeFeature = `Feature: tag filter probe

  @a
  Scenario: scenario a
    Given noop

  @b
  Scenario: scenario b
    Given noop

  @c
  Scenario: scenario c
    Given noop
`

func TestScenarioTagsGodogFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.feature")
	if err := os.WriteFile(path, []byte(tagProbeFeature), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		tags  string
		and   string
		unset bool // leave BDD_TAGS unset (use defaultTags)
		want  []string
	}{
		{
			name: "comma OR distributed with AND",
			tags: "@c,@a",
			and:  "~@c",
			want: []string{"scenario a"},
		},
		{
			name: "comma OR without AND",
			tags: "@a,@b",
			want: []string{"scenario a", "scenario b"},
		},
		{
			name:  "defaultTags with AND",
			and:   "~@c",
			unset: true,
			want:  []string{"scenario a", "scenario b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.unset {
				t.Setenv("BDD_TAGS", "")
			} else {
				t.Setenv("BDD_TAGS", tc.tags)
			}
			t.Setenv("BDD_TAGS_AND", tc.and)

			var ran []string
			init := func(ctx *godog.ScenarioContext) {
				ctx.Step(`^noop$`, func() error { return nil })
				ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
					ran = append(ran, sc.Name)
					return ctx, nil
				})
			}

			suite := godog.TestSuite{
				Name:                "tag-probe",
				ScenarioInitializer: init,
				Options: &godog.Options{
					Format:   "pretty",
					Output:   io.Discard,
					Paths:    []string{path},
					Tags:     scenarioTags(),
					Strict:   true,
					TestingT: t,
				},
			}
			if code := suite.Run(); code != 0 {
				t.Fatalf("godog exited %d", code)
			}

			sort.Strings(ran)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if len(ran) != len(want) {
				t.Fatalf("ran %v, want %v (filter %q)", ran, want, scenarioTags())
			}
			for i := range want {
				if ran[i] != want[i] {
					t.Fatalf("ran %v, want %v (filter %q)", ran, want, scenarioTags())
				}
			}
		})
	}
}
