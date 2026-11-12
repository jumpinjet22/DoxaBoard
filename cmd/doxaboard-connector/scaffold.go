package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templates embed.FS

var connectorNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

type scaffoldData struct {
	Name    string // registry name, e.g. "my-cms"
	Package string // Go package, e.g. "mycms"
	Title   string // "My Cms"
}

func newCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "connectors", "parent directory for the connector")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: doxaboard-connector new [-dir connectors] <name>")
		fmt.Fprintln(stderr, "  name: lowercase letters, digits and dashes, e.g. planning-center")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	path, err := scaffold(*dir, fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, `Created %s

Next:
  1. Edit %s: map your CMS's API to slides (search for TODO).
  2. Replace testdata/items.json with a real (scrubbed!) API response.
  3. Add a blank import to connectors/all/all.go.
  4. make connector-check NAME=%s
`, path, filepath.Join(path, filepath.Base(path)+".go"), fs.Arg(0))
	return 0
}

// scaffold writes a new connector into parent/<package> and returns its path.
func scaffold(parent, name string) (string, error) {
	if !connectorNameRE.MatchString(name) {
		return "", fmt.Errorf("invalid name %q: use lowercase letters, digits and dashes (2-31 chars)", name)
	}
	d := scaffoldData{
		Name:    name,
		Package: strings.ReplaceAll(name, "-", ""),
		Title:   titleCase(name),
	}
	path := filepath.Join(parent, d.Package)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	}
	files := map[string]string{
		"connector.go.tmpl":      d.Package + ".go",
		"connector_test.go.tmpl": d.Package + "_test.go",
		"items.json.tmpl":        filepath.Join("testdata", "items.json"),
		"README.md.tmpl":         "README.md",
	}
	for tmplName, out := range files {
		t, err := template.ParseFS(templates, "templates/"+tmplName)
		if err != nil {
			return "", err
		}
		full := filepath.Join(path, out)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		f, err := os.Create(full)
		if err != nil {
			return "", err
		}
		err = t.Execute(f, d)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", fmt.Errorf("writing %s: %w", full, err)
		}
	}
	return path, nil
}

func titleCase(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}
