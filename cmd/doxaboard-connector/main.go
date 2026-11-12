// Command doxaboard-connector helps people build DoxaBoard connectors.
//
//	doxaboard-connector check-feed <url|file|->   validate a slide feed (sidecar connectors)
//	doxaboard-connector new <name>                scaffold an in-tree Go connector
//	doxaboard-connector lint <dir>...             check a connector's imports and files
//	doxaboard-connector schema                    print the feed's JSON Schema
//	doxaboard-connector version
//
// The same checks run in DoxaBoard's CI, so if it passes here it passes there.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/jumpinjet22/doxaboard/slides/schema"
)

var version = "dev"

const usage = `doxaboard-connector: tools for building DoxaBoard connectors

Usage:
  doxaboard-connector check-feed [flags] <url | file | ->
      Validate a doxaboard.slides/v1 feed: schema, stable IDs, images.
      Run it against your sidecar connector before you list it.
  doxaboard-connector new [-dir connectors] <name>
      Scaffold an in-tree Go connector that already passes the conformance kit.
  doxaboard-connector lint <connector-dir>...
      Check a Go connector against the import rules CI enforces.
  doxaboard-connector schema
      Print the JSON Schema for the feed (validate in any language).
  doxaboard-connector version

Run "doxaboard-connector <command> -h" for a command's flags.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "check-feed":
		return checkFeedCmd(args[1:], stdout, stderr)
	case "new":
		return newCmd(args[1:], stdout, stderr)
	case "lint":
		return lintCmd(args[1:], stdout, stderr)
	case "schema":
		stdout.Write(schema.V1)
		return 0
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
