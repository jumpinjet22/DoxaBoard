// Package registry defines how content connectors plug into DoxaBoard.
//
// A connector turns some content system (Rock RMS, WordPress, an RSS feed, a
// folder of photos) into slides. Connectors register themselves at init time:
//
//	func init() { registry.Register(myConnector{}) }
//
// and are compiled into the server. Connectors in other languages don't use
// this package at all: they run as a separate service that serves the slide
// feed JSON, and DoxaBoard reads it with the "jsonfeed" connector.
//
// Connectors must do all network I/O through Deps.HTTP and all file I/O
// through Deps.Files. CI enforces this with an import allowlist.
package registry

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jumpinjet22/doxaboard/slides"
)

// Status tells admins how much to trust a connector.
type Status string

const (
	StatusOfficial     Status = "official"
	StatusCommunity    Status = "community"
	StatusExperimental Status = "experimental"
)

// FieldType controls how a config field is edited and validated.
type FieldType string

const (
	FieldText   FieldType = "text"
	FieldSecret FieldType = "secret" // write-only in the console; never sent to browsers or screens
	FieldURL    FieldType = "url"    // http(s); its host is automatically allowed for Deps.HTTP
	FieldNumber FieldType = "number"
	FieldBool   FieldType = "bool"
)

// Field describes one config setting. The admin console builds the
// "add source" form from these.
type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required,omitempty"`
	Default  string    `json:"default,omitempty"`
	Help     string    `json:"help,omitempty"`
}

// ExtraHostsKey is a config key every connector accepts: a comma-separated
// list of additional hosts Deps.HTTP may reach (e.g. an image CDN).
const ExtraHostsKey = "extra_hosts"

// Info describes a connector.
type Info struct {
	// Name is the stable identifier stored in configs: lowercase, e.g. "rock".
	Name        string  `json:"name"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      Status  `json:"status"`
	Maintainer  string  `json:"maintainer"` // GitHub handle(s)
	Fields      []Field `json:"fields"`
	// UsesFiles is true for connectors that read Deps.Files instead of the network.
	UsesFiles bool `json:"uses_files,omitempty"`
}

// Config holds a source's settings, keyed by Field.Key.
type Config map[string]string

// Deps is everything a connector may use to reach the outside world.
type Deps struct {
	// HTTP only reaches hosts allowed for this source, with timeouts and
	// redirect checks. Read bodies with ReadLimited.
	HTTP *http.Client
	// Files is the server's content directory (folder-based connectors only).
	Files fs.FS
	// Logf logs with the source's name prefixed.
	Logf func(format string, args ...any)
	// Now is the clock; use it instead of time.Now so tests can control time.
	Now func() time.Time
}

// Connector creates sources from config.
type Connector interface {
	Info() Info
	// New validates cfg and returns a ready source. It must not do I/O.
	New(cfg Config, deps Deps) (Source, error)
}

// Source produces slides.
type Source interface {
	// Fetch returns the current slides. Return an error (not an empty list)
	// when the upstream system fails, so the server keeps the last good
	// slides instead of blanking screens. Expired slides may be included;
	// the server filters them.
	//
	// Image URLs must be absolute http(s) URLs, unless the source also
	// implements Opener, in which case relative URLs are opened through it.
	Fetch(ctx context.Context) ([]slides.Slide, error)
}

// Opener is implemented by sources that serve their own images (e.g. from
// Deps.Files) instead of over HTTP.
type Opener interface {
	Open(ctx context.Context, imageURL string) (io.ReadCloser, error)
}

var (
	mu         sync.RWMutex
	connectors = map[string]Connector{}
	nameRE     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	keyRE      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
)

// Register adds a connector. It panics on an invalid or duplicate
// connector, since that's a programming error caught by any test run.
func Register(c Connector) {
	if err := CheckInfo(c.Info()); err != nil {
		panic(fmt.Sprintf("registry: %v", err))
	}
	mu.Lock()
	defer mu.Unlock()
	name := c.Info().Name
	if _, dup := connectors[name]; dup {
		panic(fmt.Sprintf("registry: connector %q registered twice", name))
	}
	connectors[name] = c
}

// Get returns a registered connector.
func Get(name string) (Connector, bool) {
	mu.RLock()
	defer mu.RUnlock()
	c, ok := connectors[name]
	return c, ok
}

// List returns all connectors' info, sorted by name.
func List() []Info {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Info, 0, len(connectors))
	for _, c := range connectors {
		out = append(out, c.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CheckInfo validates connector metadata.
func CheckInfo(in Info) error {
	if !nameRE.MatchString(in.Name) {
		return fmt.Errorf("connector name %q must match %s", in.Name, nameRE)
	}
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Description) == "" {
		return fmt.Errorf("connector %q needs a title and description", in.Name)
	}
	switch in.Status {
	case StatusOfficial, StatusCommunity, StatusExperimental:
	default:
		return fmt.Errorf("connector %q has invalid status %q", in.Name, in.Status)
	}
	if strings.TrimSpace(in.Maintainer) == "" {
		return fmt.Errorf("connector %q needs a maintainer", in.Name)
	}
	seen := map[string]bool{ExtraHostsKey: true}
	for _, f := range in.Fields {
		if !keyRE.MatchString(f.Key) {
			return fmt.Errorf("connector %q: field key %q must match %s", in.Name, f.Key, keyRE)
		}
		if seen[f.Key] {
			return fmt.Errorf("connector %q: duplicate or reserved field key %q", in.Name, f.Key)
		}
		seen[f.Key] = true
		switch f.Type {
		case FieldText, FieldSecret, FieldURL, FieldNumber, FieldBool:
		default:
			return fmt.Errorf("connector %q: field %q has invalid type %q", in.Name, f.Key, f.Type)
		}
		if strings.TrimSpace(f.Label) == "" {
			return fmt.Errorf("connector %q: field %q needs a label", in.Name, f.Key)
		}
	}
	return nil
}

// ValidateConfig checks cfg against the connector's fields and returns a
// copy with defaults applied.
func ValidateConfig(in Info, cfg Config) (Config, error) {
	out := Config{}
	for k, v := range cfg {
		out[k] = strings.TrimSpace(v)
	}
	for _, f := range in.Fields {
		v := out[f.Key]
		if v == "" && f.Default != "" {
			v = f.Default
			out[f.Key] = v
		}
		if v == "" {
			if f.Required {
				return nil, fmt.Errorf("%s is required", f.Label)
			}
			continue
		}
		switch f.Type {
		case FieldURL:
			u, err := url.Parse(v)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, fmt.Errorf("%s must be an http(s) URL", f.Label)
			}
		case FieldNumber:
			if _, err := strconv.Atoi(v); err != nil {
				return nil, fmt.Errorf("%s must be a whole number", f.Label)
			}
		case FieldBool:
			if _, err := strconv.ParseBool(v); err != nil {
				return nil, fmt.Errorf("%s must be true or false", f.Label)
			}
		}
	}
	return out, nil
}

// AllowedHosts returns the hosts a source may reach: the hosts of its URL
// fields plus any extra_hosts.
func AllowedHosts(in Info, cfg Config) []string {
	set := map[string]bool{}
	for _, f := range in.Fields {
		if f.Type != FieldURL || cfg[f.Key] == "" {
			continue
		}
		if u, err := url.Parse(cfg[f.Key]); err == nil && u.Hostname() != "" {
			set[strings.ToLower(u.Hostname())] = true
		}
	}
	for _, h := range strings.Split(cfg[ExtraHostsKey], ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			set[h] = true
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
