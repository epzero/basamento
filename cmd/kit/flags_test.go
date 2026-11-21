package main

import "testing"

func TestOptionsKeepsTheConfigPathOutOfTheSettings(t *testing.T) {
	f := newFlags("kit serve")
	if err := f.parse([]string{"--config", "kit.yaml", "--profile", "embedded"}); err != nil {
		t.Fatal(err)
	}
	opts := f.options(nil)
	if _, ok := opts.Flags["config"]; ok {
		t.Fatal("options passed the config path as a setting")
	}
	if opts.File != "kit.yaml" || opts.AutoFile != "" {
		t.Fatalf("file = %q, auto = %q", opts.File, opts.AutoFile)
	}
	if opts.Flags["profile"] != "embedded" {
		t.Fatalf("profile flag = %q", opts.Flags["profile"])
	}
}

func TestOptionsDiscoversKitYAMLWithoutAnExplicitConfig(t *testing.T) {
	f := newFlags("kit serve")
	if err := f.parse(nil); err != nil {
		t.Fatal(err)
	}
	opts := f.options(nil)
	if opts.File != "" || opts.AutoFile != "kit.yaml" {
		t.Fatalf("file = %q, auto = %q, want discovery of kit.yaml", opts.File, opts.AutoFile)
	}
}
