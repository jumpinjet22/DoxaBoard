package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jumpinjet22/doxacast/slides"
	"github.com/jumpinjet22/doxacast/slides/schema"
)

func pngBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.White)
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func runCLI(args ...string) (int, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String() + errOut.String()
}

func TestCheckFeedGood(t *testing.T) {
	big, small := pngBytes(1920, 1080), pngBytes(400, 400)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"schema":"doxacast.slides/v1","slides":[
				{"id":"a","title":"Welcome","image":{"url":"/img/hd.png","alt":"x"}},
				{"id":"b","title":"Square","image":{"url":"/img/square.png","alt":"y"}}]}`))
		case "/img/hd.png":
			w.Write(big)
		case "/img/square.png":
			w.Write(small)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	code, out := runCLI("check-feed", srv.URL+"/feed")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "400x400") || !strings.Contains(out, "✓ Feed works") {
		t.Fatalf("expected a size warning and success:\n%s", out)
	}
	// Warnings fail under -strict.
	if code, _ := runCLI("check-feed", "-strict", srv.URL+"/feed"); code != 1 {
		t.Fatalf("-strict should fail on warnings, got %d", code)
	}
}

func TestCheckFeedCatchesProblems(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/unstable":
			// New IDs on every request: the classic bug.
			i := n.Add(1)
			w.Write([]byte(`{"schema":"doxacast.slides/v1","slides":[{"id":"x` +
				string('0'+i) + `","title":"Hi"}]}`))
		case "/notimage":
			w.Write([]byte(`{"schema":"doxacast.slides/v1","slides":[{"id":"a","image":{"url":"/page","alt":"x"}}]}`))
		case "/page":
			w.Write([]byte("<html>nope</html>"))
		case "/badschema":
			w.Write([]byte(`{"slides":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cases := map[string]string{
		"/unstable":  "changed between two fetches",
		"/notimage":  "not an image",
		"/badschema": "schema: missing",
		"/missing":   "HTTP 404",
	}
	for path, want := range cases {
		code, out := runCLI("check-feed", srv.URL+path)
		if code != 1 || !strings.Contains(out, want) {
			t.Errorf("%s: want exit 1 mentioning %q, got %d:\n%s", path, want, code, out)
		}
	}
}

func TestCheckFeedJSONAndFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "feed.json")
	os.WriteFile(f, []byte(`{"schema":"doxacast.slides/v1","slides":[{"id":"a","title":"x"}]}`), 0o644)
	code, out := runCLI("check-feed", "-json", f)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var rep Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || !rep.OK || rep.Slides != 1 {
		t.Fatalf("bad report %+v (%v)\n%s", rep, err, out)
	}
}

func TestSchemaMatchesGoLimits(t *testing.T) {
	var s struct {
		Properties struct {
			Schema struct {
				Const string `json:"const"`
			} `json:"schema"`
			Slides struct {
				MaxItems int `json:"maxItems"`
			} `json:"slides"`
		} `json:"properties"`
		Defs struct {
			Slide struct {
				Properties map[string]struct {
					MaxLength int `json:"maxLength"`
					MaxItems  int `json:"maxItems"`
					Minimum   int `json:"minimum"`
					Maximum   int `json:"maximum"`
				} `json:"properties"`
			} `json:"slide"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(schema.V1, &s); err != nil {
		t.Fatal(err)
	}
	p := s.Defs.Slide.Properties
	checks := map[string][2]int{
		"schema.const":             {boolInt(s.Properties.Schema.Const == slides.SchemaV1), 1},
		"slides.maxItems":          {s.Properties.Slides.MaxItems, slides.MaxSlides},
		"id.maxLength":             {p["id"].MaxLength, slides.MaxIDLen},
		"title.maxLength":          {p["title"].MaxLength, slides.MaxTitleLen},
		"body.maxLength":           {p["body"].MaxLength, slides.MaxBodyLen},
		"tags.maxItems":            {p["tags"].MaxItems, slides.MaxTags},
		"duration_seconds.minimum": {p["duration_seconds"].Minimum, slides.MinDuration},
		"duration_seconds.maximum": {p["duration_seconds"].Maximum, slides.MaxDuration},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("JSON Schema %s = %d, Go says %d", name, c[0], c[1])
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestScaffoldPassesConformance generates a connector inside the module and
// runs its tests, proving `new` starts contributors from green.
func TestScaffoldPassesConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test in a subprocess")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.MkdirTemp(filepath.Join(root, "connectors"), "zzscaffold")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })

	path, err := scaffold(parent, "test-cms")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scaffold(parent, "test-cms"); err == nil {
		t.Fatal("scaffolding over an existing connector should fail")
	}
	if _, err := scaffold(parent, "Bad Name"); err == nil {
		t.Fatal("invalid name should fail")
	}
	if findings, err := lintDir(path); err != nil || len(findings) != 0 {
		t.Fatalf("generated connector fails lint: %v %+v", err, findings)
	}
	rel, _ := filepath.Rel(root, path)
	cmd := exec.Command("go", "test", "./"+filepath.ToSlash(rel)+"/")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated connector fails its own tests:\n%s", out)
	}
	vet := exec.Command("go", "vet", "./"+filepath.ToSlash(rel)+"/")
	vet.Dir = root
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("go vet on generated connector:\n%s", out)
	}
}
