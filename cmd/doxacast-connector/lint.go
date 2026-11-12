package main

import (
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const modulePath = "github.com/jumpinjet22/doxacast"

// forbiddenImports can't appear in connector code (tests are exempt).
// Connectors reach the network only through Deps.HTTP and files only
// through Deps.Files; these would let them go around that.
var forbiddenImports = map[string]string{
	"os":            "use deps.Files for files; config comes from registry.Config",
	"os/exec":       "connectors may not run programs",
	"os/signal":     "connectors may not handle signals",
	"io/ioutil":     "deprecated; and use deps.Files",
	"net":           "use deps.HTTP (net/http and net/url are fine)",
	"net/rpc":       "use deps.HTTP",
	"net/smtp":      "connectors may not send email",
	"unsafe":        "not allowed",
	"syscall":       "not allowed",
	"plugin":        "not allowed",
	"runtime/debug": "not allowed",
	"C":             "cgo is not allowed",
}

// allowedModuleImports are the only DoxaCast packages a connector may use.
var allowedModuleImports = map[string]bool{
	modulePath + "/connectors/registry": true,
	modulePath + "/slides":              true,
}

type lintFinding struct {
	File, Import, Reason string
	Line                 int
	Review               bool // needs human review rather than being forbidden
}

func lintCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: doxacast-connector lint <connector-dir>...")
		fmt.Fprintln(stderr, "  Checks connector code against the import rules CI enforces.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	failed := false
	for _, dir := range fs.Args() {
		findings, err := lintDir(dir)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", dir, err)
			failed = true
			continue
		}
		for _, f := range findings {
			mark := "✗"
			if f.Review {
				mark = "⚠"
			} else {
				failed = true
			}
			fmt.Fprintf(stdout, "%s %s:%d: import %q: %s\n", mark, f.File, f.Line, f.Import, f.Reason)
		}
		if len(findings) == 0 {
			fmt.Fprintf(stdout, "✓ %s: imports OK\n", dir)
		}
	}
	if failed {
		return 1
	}
	return 0
}

// lintDir checks non-test Go files in dir (not recursive into testdata).
func lintDir(dir string) ([]lintFinding, error) {
	var findings []lintFinding
	required := []string{"README.md"}
	for _, r := range required {
		if _, err := os.Stat(filepath.Join(dir, r)); err != nil {
			findings = append(findings, lintFinding{File: filepath.Join(dir, r), Reason: "required file is missing"})
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	hasTest, hasCode := false, false
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			hasTest = true
			continue
		}
		hasCode = true
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			line := fset.Position(imp.Pos()).Line
			if reason, bad := forbiddenImports[p]; bad {
				findings = append(findings, lintFinding{File: path, Line: line, Import: p, Reason: reason})
				continue
			}
			switch {
			case strings.HasPrefix(p, modulePath+"/"):
				if !allowedModuleImports[p] {
					findings = append(findings, lintFinding{File: path, Line: line, Import: p,
						Reason: "connectors may only import connectors/registry and slides from DoxaCast"})
				}
			case strings.Contains(strings.SplitN(p, "/", 2)[0], "."):
				findings = append(findings, lintFinding{File: path, Line: line, Import: p, Review: true,
					Reason: "third-party dependency: a maintainer must review it (license, security, upkeep)"})
			}
		}
	}
	if !hasCode {
		findings = append(findings, lintFinding{File: dir, Reason: "no Go files"})
	}
	if !hasTest {
		findings = append(findings, lintFinding{File: dir, Reason: "no tests: connectors must run connectortest.Run"})
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].File < findings[j].File })
	return findings, nil
}
