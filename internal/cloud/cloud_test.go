package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRclone stands in for `rclone rcd`: the remote-control API on a unix
// socket, the sign-in listener, and the log the sign-in link is written to.
type fakeRclone struct {
	t       *testing.T
	mu      sync.Mutex
	remotes map[string]string // name -> type
	synced  []map[string]any
	deleted []string
	photos  map[string][]string // album -> files already there
	code    chan string
	auth    *httptest.Server
	logFile string
	failTok bool
}

func newFake(t *testing.T) (*fakeRclone, *Manager) {
	t.Helper()
	dir, err := os.MkdirTemp("", "rc") // short path: unix socket names are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeRclone{t: t, remotes: map[string]string{}, photos: map[string][]string{}, code: make(chan string, 1),
		logFile: filepath.Join(dir, "rclone.log")}
	os.WriteFile(f.logFile, []byte("earlier log line http://127.0.0.1:53682/auth?state=STALE\n"), 0o600)

	f.auth = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth" {
			http.Redirect(w, r, "https://login.example/authorize?state="+r.URL.Query().Get("state"), http.StatusTemporaryRedirect)
			return
		}
		f.code <- r.URL.Query().Get("code")
	}))
	t.Cleanup(f.auth.Close)

	sock := filepath.Join(dir, "rc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(f.rc)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	m := &Manager{Socket: sock, LogFile: f.logFile, PhotoRoot: "/photos", StateFile: filepath.Join(dir, "cloud.json"),
		AuthAddr: strings.TrimPrefix(f.auth.URL, "http://")}
	return f, m
}

func (f *fakeRclone) rc(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	json.NewDecoder(r.Body).Decode(&in)
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		reply(map[string]any{"error": msg})
	}
	str := func(k string) string { s, _ := in[k].(string); return s }
	f.mu.Lock()
	defer f.mu.Unlock()
	switch strings.TrimPrefix(r.URL.Path, "/") {
	case "rc/noop":
		reply(map[string]any{})
	case "config/listremotes":
		names := []string{}
		for n := range f.remotes {
			names = append(names, n)
		}
		reply(map[string]any{"remotes": names})
	case "config/get":
		reply(map[string]any{"type": f.remotes[str("name")]})
	case "config/delete":
		delete(f.remotes, str("name"))
		f.deleted = append(f.deleted, str("name"))
		reply(map[string]any{})
	case "config/create":
		opt, _ := in["opt"].(map[string]any)
		ask := func(state, name, def string) {
			reply(map[string]any{"State": state, "Option": map[string]any{"Name": name, "DefaultStr": def}})
		}
		switch state, _ := opt["state"].(string); state {
		case "": // new remote: one warm-up question, like Google Drive asks
			f.remotes[str("name")] = str("type")
			ask("warmup", "config_shared_client_id", "true")
		case "warmup":
			ask("islocal", browserQuestion, "true")
		case "islocal": // blocks until the browser comes back, as rclone does
			if opt["result"] != "true" {
				fail(500, "expected yes to the browser question")
				return
			}
			fh, _ := os.OpenFile(f.logFile, os.O_APPEND|os.O_WRONLY, 0o600)
			fmt.Fprintf(fh, "NOTICE: please go to the following link: http://127.0.0.1:53682/auth?state=S123\n")
			fh.Close()
			f.mu.Unlock()
			var code string
			select {
			case code = <-f.code:
			case <-r.Context().Done():
			}
			f.mu.Lock()
			if code == "" || f.failTok {
				fail(500, `failed to get token: oauth2: "invalid_grant"`)
				return
			}
			ask("pickdrive", "config_driveid", "drive-1")
		case "pickdrive":
			if opt["result"] != "drive-1" {
				fail(500, "expected the default drive")
				return
			}
			reply(map[string]any{"State": ""})
		}
	case "operations/list":
		if str("fs") == "/photos" {
			files, ok := f.photos[str("remote")]
			if !ok {
				fail(404, "error in ListJSON: directory not found")
				return
			}
			list := []map[string]any{}
			for _, n := range files {
				list = append(list, map[string]any{"Name": n})
			}
			reply(map[string]any{"list": list})
			return
		}
		reply(map[string]any{"list": []map[string]any{{"Name": "Lobby", "IsDir": true}, {"Name": "archive", "IsDir": true}}})
	case "sync/sync":
		f.synced = append(f.synced, in)
		reply(map[string]any{})
	default:
		fail(404, "unknown method "+r.URL.Path)
	}
}

func TestConnectBrowseAndSync(t *testing.T) {
	f, m := newFake(t)
	ctx := context.Background()
	if err := m.Available(ctx); err != nil {
		t.Fatal(err)
	}

	authURL, err := m.Connect(ctx, "onedrive")
	if err != nil {
		t.Fatal(err)
	}
	// The admin gets the provider's page, and the stale link in the log is ignored.
	if authURL != "https://login.example/authorize?state=S123" {
		t.Fatalf("auth URL %q", authURL)
	}
	if typ, u := m.Pending(); typ != "onedrive" || u != authURL {
		t.Errorf("pending %q %q", typ, u)
	}
	if drives, _ := m.Drives(ctx); len(drives) != 0 {
		t.Errorf("a half-connected drive shouldn't be listed: %v", drives)
	}

	for _, bad := range []string{"", "not a url", "http://localhost:53682/", "http://localhost:53682/?code=abc&state=OTHER",
		"http://localhost:53682/?error=access_denied&error_description=You+said+no&state=S123"} {
		if _, err := m.Finish(ctx, bad); err == nil {
			t.Errorf("Finish(%q) should fail", bad)
		}
	}
	drive, err := m.Finish(ctx, " http://localhost:53682/?code=abc&state=S123 ")
	if err != nil {
		t.Fatal(err)
	}
	if drive.Name != "onedrive" || drive.Title != "OneDrive or SharePoint" {
		t.Errorf("drive %+v", drive)
	}
	if drives, _ := m.Drives(ctx); len(drives) != 1 {
		t.Fatalf("drives %v", drives)
	}
	if _, u := m.Pending(); u != "" {
		t.Error("sign-in should be finished")
	}

	if got, err := m.Browse(ctx, "onedrive", "/Signage/"); err != nil || strings.Join(got, ",") != "archive,Lobby" {
		t.Errorf("browse %v %v", got, err)
	}
	if _, err := m.Browse(ctx, "onedrive", "a/../b"); err == nil {
		t.Error("paths with .. should be refused")
	}

	// Mirroring deletes what isn't in the cloud, so an album that already
	// has photos is off limits.
	f.mu.Lock()
	f.photos["Mine"] = []string{"family.jpg"}
	f.mu.Unlock()
	if _, err := m.AddFolder(ctx, "onedrive", "Signage/Lobby", "Mine"); err == nil {
		t.Error("an album with photos in it must be refused")
	}
	for _, bad := range []string{"", "../x", "a/b", ".hidden"} {
		if _, err := m.AddFolder(ctx, "onedrive", "x", bad); err == nil {
			t.Errorf("album %q should be refused", bad)
		}
	}
	if _, err := m.AddFolder(ctx, "nope", "x", "Lobby"); err == nil {
		t.Error("unknown drive should be refused")
	}
	folder, err := m.AddFolder(ctx, "onedrive", "/Signage/Lobby/", "Lobby")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddFolder(ctx, "onedrive", "Other", "lobby"); err == nil {
		t.Error("two folders can't fill the same album")
	}
	waitFor(t, func() bool { fs := m.Folders(); return len(fs) == 1 && !fs[0].LastSync.IsZero() && !fs[0].Syncing })
	f.mu.Lock()
	call := f.synced[0]
	f.mu.Unlock()
	if call["srcFs"] != "onedrive:Signage/Lobby" || call["dstFs"] != "/photos/Lobby" {
		t.Errorf("sync %v -> %v", call["srcFs"], call["dstFs"])
	}
	if cfg, _ := call["_config"].(map[string]any); cfg["MaxDepth"] != float64(1) {
		t.Errorf("sync should stay at the top level: %v", call["_config"])
	}

	// The folder list survives a restart.
	again := &Manager{Socket: m.Socket, LogFile: m.LogFile, PhotoRoot: "/photos", StateFile: m.StateFile}
	if fs := again.Folders(); len(fs) != 1 || fs[0].Album != "Lobby" {
		t.Errorf("reloaded folders %+v", fs)
	}

	if err := m.RemoveDrive(ctx, "onedrive"); err == nil {
		t.Error("a drive with folders can't be disconnected")
	}
	if err := m.RemoveFolder(folder.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveDrive(ctx, "onedrive"); err != nil {
		t.Fatal(err)
	}
	if drives, _ := m.Drives(ctx); len(drives) != 0 {
		t.Errorf("drives after disconnect: %v", drives)
	}
}

func TestFailedAndCancelledSignInLeaveNothingBehind(t *testing.T) {
	f, m := newFake(t)
	ctx := context.Background()

	f.failTok = true
	if _, err := m.Connect(ctx, "dropbox"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Finish(ctx, "http://localhost:53682/?code=expired&state=S123"); err == nil {
		t.Fatal("a rejected code should fail")
	}
	f.failTok = false

	if _, err := m.Connect(ctx, "dropbox"); err != nil {
		t.Fatal(err)
	}
	m.Cancel(ctx)
	if _, err := m.Finish(ctx, "http://localhost:53682/?code=abc&state=S123"); err != ErrNoSignIn {
		t.Errorf("after cancel: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.remotes) != 0 || len(f.deleted) < 2 {
		t.Errorf("half-made drives left behind: %v (deleted %v)", f.remotes, f.deleted)
	}
}

func TestUnavailable(t *testing.T) {
	m := &Manager{Socket: filepath.Join(t.TempDir(), "nothing.sock"), StateFile: filepath.Join(t.TempDir(), "c.json")}
	if err := m.Available(context.Background()); err == nil || !strings.Contains(err.Error(), "rclone") {
		t.Errorf("want a clear error, got %v", err)
	}
	if _, err := m.Connect(context.Background(), "ftp"); err == nil {
		t.Error("unknown provider should fail")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}
