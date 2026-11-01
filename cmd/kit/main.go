// Command kit is the single binary: serve, run, validate, replay.
// Status: skeleton; subcommands land with their work packages.
package main

import (
	"fmt"
	"os"
)

var version = "0.0.0-dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	fmt.Fprintln(os.Stderr, "kit: no subcommands yet; see docs/roadmap/README.md")
	os.Exit(2)
}
