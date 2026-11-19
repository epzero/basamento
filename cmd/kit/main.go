// Command kit is the single binary. It serves the embedded or cloud profile
// and validates configuration; run, worker and replay land with their work
// packages.
package main

import (
	"fmt"
	"os"
)

var version = "0.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "serve":
		runServe(os.Args[2:])
	case "worker":
		runWorker(os.Args[2:])
	case "config":
		if len(os.Args) < 3 || os.Args[2] != "validate" {
			usage()
			os.Exit(2)
		}
		runConfigValidate(os.Args[3:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "kit: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: kit <version|serve|worker|config validate> [flags]")
}
