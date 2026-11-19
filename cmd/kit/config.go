package main

import (
	"fmt"
	"os"

	"github.com/epzero/basamento/internal/config"
)

// runConfigValidate prints the effective configuration with its sources and
// exits 0, or prints every validation error and exits 2 (REQ-CONFIG-018,
// REQ-CONFIG-019).
func runConfigValidate(args []string) {
	f := newFlags("kit config validate")
	if err := f.parse(args); err != nil {
		os.Exit(2)
	}
	result, err := config.Load(f.options(os.Stdin))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if errs := result.Validate(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, e)
		}
		os.Exit(2)
	}
	result.Report(os.Stdout)
}
