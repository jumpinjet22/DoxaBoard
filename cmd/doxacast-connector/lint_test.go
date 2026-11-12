package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintCatchesForbiddenImports(t *testing.T) {
	dir := t.TempDir()
	src := "package bad\n\nimport (\n" +
		"\t\"os/exec\"\n" +
		"\t\"net\"\n" +
		"\t\"github.com/jumpinjet22/doxacast/internal/server\"\n" +
		"\t\"github.com/someone/somelib\"\n" +
		"\t\"net/http\"\n" +
		")\n"
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := lintDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]lintFinding{}
	for _, f := range findings {
		if f.Import != "" {
			got[f.Import] = f
		}
	}
	for _, imp := range []string{"os/exec", "net", "github.com/jumpinjet22/doxacast/internal/server", "github.com/someone/somelib"} {
		if _, ok := got[imp]; !ok {
			t.Errorf("expected a finding for %s", imp)
		}
	}
	if _, ok := got["net/http"]; ok {
		t.Error("net/http is allowed")
	}
	if !got["github.com/someone/somelib"].Review || got["os/exec"].Review {
		t.Error("third-party should be review-only; os/exec should be forbidden")
	}
	code, out := runCLI("lint", dir)
	if code != 1 || !strings.Contains(out, "README.md") || !strings.Contains(out, "no tests") {
		t.Fatalf("lint exit %d:\n%s", code, out)
	}
}
