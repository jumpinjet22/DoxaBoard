// Package cloud connects cloud drives (OneDrive, Google Drive, Dropbox) and
// keeps chosen folders mirrored into the photo directory as albums.
//
// The work is done by an rclone daemon (rclone rcd) that DoxaBoard drives
// over its remote-control API on a unix socket. The point of this package
// is the sign-in: an admin connects a drive from the console in a browser,
// with no terminal and no rclone on their own computer.
//
// Sign-in works like this. rclone's OAuth apps redirect to
// http://localhost:53682/, a listener rclone opens on the server. The
// admin's browser is on another machine, so after approving access it lands
// on a localhost page that can't load. The admin pastes that page's address
// into the console and DoxaBoard replays it to rclone's listener, which
// completes the exchange. Tokens never pass through the browser or DoxaBoard:
// rclone gets them straight from the provider and keeps them in its config.
package cloud

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Provider is a kind of drive the console offers to connect.
type Provider struct {
	Type  string `json:"type"` // rclone backend name
	Title string `json:"title"`
	// params ask for read-only access where the backend supports it:
	// signage only ever reads.
	params map[string]string
}

// Providers are offered in this order.
var Providers = []Provider{
	{Type: "onedrive", Title: "OneDrive or SharePoint",
		params: map[string]string{"access_scopes": "Files.Read Files.Read.All Sites.Read.All offline_access"}},
	{Type: "drive", Title: "Google Drive", params: map[string]string{"scope": "drive.readonly"}},
	{Type: "dropbox", Title: "Dropbox"},
}

func provider(typ string) (Provider, bool) {
	for _, p := range Providers {
		if p.Type == typ {
			return p, true
		}
	}
	return Provider{}, false
}

// Drive is a connected account (an rclone remote).
type Drive struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// Folder is a cloud folder mirrored into an album.
type Folder struct {
	ID        string    `json:"id"`
	Drive     string    `json:"drive"`
	Path      string    `json:"path"`
	Album     string    `json:"album"`
	LastSync  time.Time `json:"last_sync"`
	LastError string    `json:"last_error,omitempty"`
	Syncing   bool      `json:"syncing,omitempty"`
}

// Errors the console shows as they are.
var (
	ErrUnavailable = errors.New("the cloud drive service (rclone) isn't running; check `docker compose ps rclone`")
	ErrNoSignIn    = errors.New("there's no sign-in in progress; start again")
	ErrNotFound    = errors.New("not found")
)

// Manager talks to the rclone daemon. Set the fields, then call Run.
type Manager struct {
	// Socket is rclone's remote-control unix socket.
	Socket string
	// LogFile is rclone's log; the sign-in link is read from it.
	LogFile string
	// PhotoRoot is the photo directory as rclone sees it, e.g. /photos.
	PhotoRoot string
	// StateFile stores the folder list.
	StateFile string
	// AuthAddr is rclone's sign-in listener. Default 127.0.0.1:53682.
	AuthAddr string
	// SyncEvery is how often folders are mirrored. Default 10 minutes.
	SyncEvery time.Duration
	Logf      func(format string, args ...any)

	once    sync.Once
	rcHTTP  *http.Client
	mu      sync.Mutex
	folders []Folder
	syncing map[string]bool
	pending *signIn
	syncMu  sync.Mutex // one sync at a time: gentle on small servers
}

// signIn is an OAuth flow waiting for the admin.
type signIn struct {
	name, typ string
	state     string
	authURL   string
	cancel    context.CancelFunc
	done      chan error
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

func (m *Manager) authAddr() string {
	if m.AuthAddr != "" {
		return m.AuthAddr
	}
	return "127.0.0.1:53682"
}

func (m *Manager) init() {
	m.once.Do(func() {
		m.syncing = map[string]bool{}
		m.rcHTTP = &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", m.Socket)
			},
		}}
		var st struct {
			Folders []Folder `json:"folders"`
		}
		if b, err := os.ReadFile(m.StateFile); err == nil {
			if err := json.Unmarshal(b, &st); err != nil {
				m.logf("cloud: %s is unreadable, starting with no folders: %v", m.StateFile, err)
			}
		}
		m.folders = st.Folders
	})
}

// rc calls one remote-control method.
func (m *Manager) rc(ctx context.Context, method string, in, out any) error {
	m.init()
	if in == nil {
		in = struct{}{}
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://rclone/"+method, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.rcHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w (%v)", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return &rcError{status: resp.StatusCode, msg: e.Error}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type rcError struct {
	status int
	msg    string
}

func (e *rcError) Error() string { return e.msg }

// Available reports whether the rclone daemon answers.
func (m *Manager) Available(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return m.rc(ctx, "rc/noop", nil, nil)
}

// Drives lists the connected accounts.
func (m *Manager) Drives(ctx context.Context) ([]Drive, error) {
	var list struct {
		Remotes []string `json:"remotes"`
	}
	if err := m.rc(ctx, "config/listremotes", nil, &list); err != nil {
		return nil, err
	}
	m.mu.Lock()
	skip := ""
	if m.pending != nil {
		skip = m.pending.name // half-made until sign-in finishes
	}
	m.mu.Unlock()
	out := []Drive{}
	for _, name := range list.Remotes {
		if name == skip {
			continue
		}
		var cfg struct {
			Type string `json:"type"`
		}
		if err := m.rc(ctx, "config/get", map[string]string{"name": name}, &cfg); err != nil {
			return nil, err
		}
		d := Drive{Name: name, Type: cfg.Type, Title: cfg.Type}
		if p, ok := provider(cfg.Type); ok {
			d.Title = p.Title
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// question is one step of rclone's config state machine.
type question struct {
	State  string `json:"State"`
	Error  string `json:"Error"`
	Option *struct {
		Name       string `json:"Name"`
		DefaultStr string `json:"DefaultStr"`
		Examples   []struct {
			Value string `json:"Value"`
		} `json:"Examples"`
	} `json:"Option"`
}

func (m *Manager) configStep(ctx context.Context, name, typ string, params map[string]string, state, result string) (question, error) {
	opt := map[string]any{"nonInteractive": true}
	if state != "" {
		opt["continue"], opt["state"], opt["result"] = true, state, result
	}
	if params == nil {
		params = map[string]string{}
	}
	var q question
	err := m.rc(ctx, "config/create", map[string]any{"name": name, "type": typ, "parameters": params, "opt": opt}, &q)
	if err == nil && q.Error != "" {
		err = errors.New(q.Error)
	}
	return q, err
}

// defaultAnswer accepts what rclone suggests: the default drive, the
// default drive type, "yes this is right".
func defaultAnswer(q question) string {
	if q.Option.DefaultStr != "" {
		return q.Option.DefaultStr
	}
	if len(q.Option.Examples) > 0 {
		return q.Option.Examples[0].Value
	}
	return ""
}

const (
	browserQuestion = "config_is_local" // "use a web browser to sign in?"
	maxConfigSteps  = 25
	signInTimeout   = 15 * time.Minute
)

var authLinkRE = regexp.MustCompile(`http://(?:127\.0\.0\.1|localhost):\d+/auth\?state=([A-Za-z0-9_-]+)`)

// Connect starts signing in to a new drive and returns the provider's
// sign-in page for the admin to open. Finish completes it.
func (m *Manager) Connect(ctx context.Context, typ string) (string, error) {
	p, ok := provider(typ)
	if !ok {
		return "", fmt.Errorf("unknown kind of drive %q", typ)
	}
	m.Cancel(ctx) // only one sign-in at a time: rclone has one listener
	drives, err := m.Drives(ctx)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, d := range drives {
		taken[d.Name] = true
	}
	name := typ
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", typ, i)
	}

	logAt := int64(0)
	if fi, err := os.Stat(m.LogFile); err == nil {
		logAt = fi.Size()
	}
	fail := func(err error) (string, error) {
		m.deleteRemote(name)
		return "", err
	}

	// Answer rclone's opening questions until it asks about the browser.
	q, err := m.configStep(ctx, name, typ, p.params, "", "")
	for i := 0; err == nil && (q.Option == nil || q.Option.Name != browserQuestion); i++ {
		if q.State == "" || q.Option == nil || i >= maxConfigSteps {
			return fail(fmt.Errorf("rclone didn't offer a browser sign-in for %s", p.Title))
		}
		q, err = m.configStep(ctx, name, typ, nil, q.State, defaultAnswer(q))
	}
	if err != nil {
		return fail(err)
	}

	// Saying yes makes rclone open its listener and wait for the admin. The
	// call returns once they've signed in; then accept the remaining defaults.
	flowCtx, cancel := context.WithTimeout(context.Background(), signInTimeout)
	s := &signIn{name: name, typ: typ, cancel: cancel, done: make(chan error, 1)}
	go func() {
		q, err := m.configStep(flowCtx, name, typ, nil, q.State, "true")
		for i := 0; err == nil && q.State != ""; i++ {
			if q.Option == nil || i >= maxConfigSteps {
				err = errors.New("rclone's setup for this drive didn't finish")
				break
			}
			q, err = m.configStep(flowCtx, name, typ, nil, q.State, defaultAnswer(q))
		}
		s.done <- err
	}()

	// rclone only reports its sign-in link in the log.
	deadline := time.Now().Add(15 * time.Second)
	var link string
	for link == "" {
		select {
		case err := <-s.done:
			cancel()
			if err == nil {
				err = errors.New("rclone finished without asking anyone to sign in")
			}
			return fail(err)
		case <-ctx.Done():
			cancel()
			<-s.done
			return fail(ctx.Err())
		case <-time.After(150 * time.Millisecond):
		}
		if hit := authLinkRE.FindStringSubmatch(readFrom(m.LogFile, logAt)); hit != nil {
			link, s.state = hit[0], hit[1]
		} else if time.Now().After(deadline) {
			cancel()
			<-s.done
			return fail(errors.New("rclone didn't start a sign-in (no link in its log)"))
		}
	}

	// The link is a redirect to the provider; hand the admin that address.
	noFollow := &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	lu, _ := url.Parse(link)
	lu.Host = m.authAddr()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, lu.String(), nil)
	resp, err := noFollow.Do(req)
	if err == nil {
		resp.Body.Close()
		s.authURL = resp.Header.Get("Location")
	}
	if au, perr := url.Parse(s.authURL); err != nil || perr != nil || au.Scheme != "https" || au.Host == "" {
		cancel()
		<-s.done
		return fail(fmt.Errorf("couldn't get the sign-in page from rclone (%v)", err))
	}

	m.mu.Lock()
	m.pending = s
	m.mu.Unlock()
	go func() { // tidy up if nobody finishes
		<-flowCtx.Done()
		m.mu.Lock()
		abandoned := m.pending == s
		if abandoned {
			m.pending = nil
		}
		m.mu.Unlock()
		if abandoned {
			m.deleteRemote(name)
		}
	}()
	return s.authURL, nil
}

func readFrom(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() < offset {
		offset = 0 // the log was rotated
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	return string(b)
}

// Pending returns the sign-in page of the flow in progress, if any.
func (m *Manager) Pending() (typ, authURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		return "", ""
	}
	return m.pending.typ, m.pending.authURL
}

// Finish completes a sign-in. pasted is the address of the page the
// admin's browser ended on (http://localhost:53682/?code=...&state=...).
func (m *Manager) Finish(ctx context.Context, pasted string) (Drive, error) {
	m.mu.Lock()
	s := m.pending
	m.mu.Unlock()
	if s == nil {
		return Drive{}, ErrNoSignIn
	}
	u, err := url.Parse(strings.TrimSpace(pasted))
	if err != nil || u.RawQuery == "" {
		return Drive{}, errors.New("that doesn't look like the address from your browser. Copy the whole address, starting with http://localhost:53682/ or http://127.0.0.1:53682/")
	}
	q := u.Query()
	if e := q.Get("error"); e != "" {
		msg := q.Get("error_description")
		if msg == "" {
			msg = e
		}
		return Drive{}, fmt.Errorf("the sign-in was refused: %s", msg)
	}
	if q.Get("code") == "" {
		return Drive{}, errors.New("that address has no sign-in code in it. Copy the address of the page you landed on after approving access")
	}
	if q.Get("state") != s.state {
		return Drive{}, errors.New("that address is from a different sign-in. Start again and use the newest link")
	}

	// Only the query is replayed, and only to rclone's own listener.
	replay := url.URL{Scheme: "http", Host: m.authAddr(), Path: "/", RawQuery: u.RawQuery}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, replay.String(), nil)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Drive{}, fmt.Errorf("couldn't hand the sign-in to rclone: %v", err)
	}
	resp.Body.Close()

	select {
	case err = <-s.done:
	case <-time.After(90 * time.Second):
		err = errors.New("rclone took too long to finish setting up the drive")
	case <-ctx.Done():
		err = ctx.Err()
	}
	m.mu.Lock()
	if m.pending == s {
		m.pending = nil
	}
	m.mu.Unlock()
	s.cancel()
	if err != nil {
		m.deleteRemote(s.name)
		return Drive{}, fmt.Errorf("the provider didn't accept the sign-in: %v", err)
	}
	p, _ := provider(s.typ)
	m.logf("cloud: connected %s as %q", p.Title, s.name)
	return Drive{Name: s.name, Type: s.typ, Title: p.Title}, nil
}

// Cancel abandons the sign-in in progress, if any.
func (m *Manager) Cancel(context.Context) {
	m.mu.Lock()
	s := m.pending
	m.pending = nil
	m.mu.Unlock()
	if s == nil {
		return
	}
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
	m.deleteRemote(s.name)
}

func (m *Manager) deleteRemote(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.rc(ctx, "config/delete", map[string]string{"name": name}, nil)
}

// RemoveDrive disconnects an account. Folders using it must go first.
func (m *Manager) RemoveDrive(ctx context.Context, name string) error {
	m.init()
	m.mu.Lock()
	for _, f := range m.folders {
		if f.Drive == name {
			m.mu.Unlock()
			return fmt.Errorf("the folder for album %q still uses this drive; remove it first", f.Album)
		}
	}
	m.mu.Unlock()
	drives, err := m.Drives(ctx)
	if err != nil {
		return err
	}
	for _, d := range drives {
		if d.Name == name {
			return m.rc(ctx, "config/delete", map[string]string{"name": name}, nil)
		}
	}
	return ErrNotFound
}

var driveNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// cleanPath normalizes a folder path inside a drive.
func cleanPath(p string) (string, error) {
	p = strings.Trim(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "/")
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." {
			return "", errors.New("folder paths can't contain . or ..")
		}
	}
	if len(p) > 1000 || strings.ContainsAny(p, "\x00\r\n") {
		return "", errors.New("invalid folder path")
	}
	return p, nil
}

// Browse lists the folders inside path on a drive.
func (m *Manager) Browse(ctx context.Context, drive, path string) ([]string, error) {
	if !driveNameRE.MatchString(drive) {
		return nil, ErrNotFound
	}
	path, err := cleanPath(path)
	if err != nil {
		return nil, err
	}
	names, err := m.listDir(ctx, drive+":", path, true)
	if err != nil {
		return nil, err
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names, nil
}

func (m *Manager) listDir(ctx context.Context, fs, remote string, dirsOnly bool) ([]string, error) {
	var out struct {
		List []struct {
			Name  string `json:"Name"`
			IsDir bool   `json:"IsDir"`
		} `json:"list"`
	}
	err := m.rc(ctx, "operations/list", map[string]any{"fs": fs, "remote": remote, "opt": map[string]bool{"dirsOnly": dirsOnly}}, &out)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.List))
	for _, e := range out.List {
		names = append(names, e.Name)
	}
	return names, nil
}

var albumRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,63}$`)

// Folders lists the mirrored folders.
func (m *Manager) Folders() []Folder {
	m.init()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Folder, len(m.folders))
	for i, f := range m.folders {
		f.Syncing = m.syncing[f.ID]
		out[i] = f
	}
	return out
}

func (m *Manager) save() error { // callers hold m.mu
	b, err := json.MarshalIndent(struct {
		Folders []Folder `json:"folders"`
	}{m.folders}, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.StateFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.StateFile)
}

// AddFolder mirrors drive:path into a new album and starts the first sync.
func (m *Manager) AddFolder(ctx context.Context, drive, path, album string) (Folder, error) {
	m.init()
	album = strings.TrimSpace(album)
	if !albumRE.MatchString(album) {
		return Folder{}, errors.New("album names can use letters, numbers, spaces, - and _ (up to 64 characters)")
	}
	path, err := cleanPath(path)
	if err != nil {
		return Folder{}, err
	}
	drives, err := m.Drives(ctx)
	if err != nil {
		return Folder{}, err
	}
	found := false
	for _, d := range drives {
		found = found || d.Name == drive
	}
	if !found {
		return Folder{}, fmt.Errorf("drive %q isn't connected", drive)
	}
	// Mirroring deletes whatever isn't in the cloud folder, so never point
	// it at an album that already has someone's photos in it.
	if existing, err := m.listDir(ctx, m.PhotoRoot, album, false); err == nil && len(existing) > 0 {
		return Folder{}, fmt.Errorf("an album called %q already has photos in it. Pick another name, or empty that album first", album)
	}
	id := make([]byte, 6)
	_, _ = rand.Read(id)
	f := Folder{ID: "c_" + hex.EncodeToString(id), Drive: drive, Path: path, Album: album}

	m.mu.Lock()
	for _, o := range m.folders {
		if strings.EqualFold(o.Album, album) {
			m.mu.Unlock()
			return Folder{}, fmt.Errorf("another cloud folder already fills the album %q", o.Album)
		}
	}
	m.folders = append(m.folders, f)
	err = m.save()
	m.mu.Unlock()
	if err != nil {
		return Folder{}, err
	}
	go m.Sync(context.Background(), f.ID)
	return f, nil
}

// RemoveFolder stops mirroring a folder. The album's photos stay where
// they are, so screens showing it don't lose anything.
func (m *Manager) RemoveFolder(id string) error {
	m.init()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, f := range m.folders {
		if f.ID == id {
			m.folders = append(m.folders[:i:i], m.folders[i+1:]...)
			return m.save()
		}
	}
	return ErrNotFound
}

// Sync mirrors one folder now.
func (m *Manager) Sync(ctx context.Context, id string) error {
	m.init()
	m.mu.Lock()
	var f *Folder
	for i := range m.folders {
		if m.folders[i].ID == id {
			c := m.folders[i]
			f = &c
		}
	}
	if f == nil || m.syncing[id] {
		m.mu.Unlock()
		if f == nil {
			return ErrNotFound
		}
		return nil // already running
	}
	m.syncing[id] = true
	m.mu.Unlock()

	m.syncMu.Lock()
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	err := m.rc(ctx, "sync/sync", map[string]any{
		"srcFs": f.Drive + ":" + f.Path,
		"dstFs": filepath.Join(m.PhotoRoot, f.Album),
		// Pictures only, top level only: an album is a flat list of images.
		"_filter": map[string]any{"IncludeRule": []string{"*.{jpg,jpeg,png,webp,gif}"}, "IgnoreCase": true, "MaxSize": 50 << 20},
		"_config": map[string]any{"Transfers": 4, "MaxDepth": 1},
	}, nil)
	cancel()
	m.syncMu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.syncing, id)
	for i := range m.folders {
		if m.folders[i].ID != id {
			continue
		}
		m.folders[i].LastSync = time.Now().UTC()
		m.folders[i].LastError = ""
		if err != nil {
			m.folders[i].LastError = friendly(err)
			m.logf("cloud: syncing album %q failed: %v", f.Album, err)
		}
		_ = m.save()
	}
	return err
}

// friendly shortens rclone's errors and explains the common ones.
func friendly(err error) string {
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case errors.Is(err, ErrUnavailable):
		return ErrUnavailable.Error()
	case strings.Contains(low, "directory not found"):
		return "The folder wasn't found on the drive. Was it renamed or moved?"
	case strings.Contains(low, "invalid_grant"), strings.Contains(low, "token expired"), strings.Contains(low, "unauthorized"):
		return "The drive's sign-in has expired or was revoked. Connect it again."
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// Run mirrors every folder on a schedule until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	m.init()
	every := m.SyncEvery
	if every <= 0 {
		every = 10 * time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if m.Available(ctx) == nil {
			for _, f := range m.Folders() {
				if ctx.Err() != nil {
					return
				}
				_ = m.Sync(ctx, f.ID)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
