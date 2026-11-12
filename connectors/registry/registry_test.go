package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type stub struct{ info Info }

func (s stub) Info() Info { return s.info }
func (s stub) New(Config, Deps) (Source, error) {
	return nil, errors.New("unused")
}

func goodInfo(name string) Info {
	return Info{
		Name: name, Title: "Test", Description: "A test connector", Status: StatusExperimental,
		Maintainer: "@someone",
		Fields: []Field{
			{Key: "base_url", Label: "Base URL", Type: FieldURL, Required: true},
			{Key: "api_key", Label: "API key", Type: FieldSecret},
			{Key: "limit", Label: "Limit", Type: FieldNumber, Default: "20"},
		},
	}
}

func TestRegisterAndList(t *testing.T) {
	Register(stub{goodInfo("zz-test-a")})
	if _, ok := Get("zz-test-a"); !ok {
		t.Fatal("not registered")
	}
	found := false
	for _, in := range List() {
		found = found || in.Name == "zz-test-a"
	}
	if !found {
		t.Fatal("not listed")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration should panic")
		}
	}()
	Register(stub{goodInfo("zz-test-a")})
}

func TestCheckInfo(t *testing.T) {
	bad := []func(*Info){
		func(i *Info) { i.Name = "Bad Name" },
		func(i *Info) { i.Title = "" },
		func(i *Info) { i.Status = "great" },
		func(i *Info) { i.Maintainer = "" },
		func(i *Info) { i.Fields = append(i.Fields, Field{Key: "base_url", Label: "dup", Type: FieldText}) },
		func(i *Info) { i.Fields = append(i.Fields, Field{Key: ExtraHostsKey, Label: "x", Type: FieldText}) },
		func(i *Info) { i.Fields = append(i.Fields, Field{Key: "x", Label: "x", Type: "color"}) },
	}
	for n, mut := range bad {
		in := goodInfo("ok")
		mut(&in)
		if CheckInfo(in) == nil {
			t.Errorf("case %d: expected error", n)
		}
	}
}

func TestValidateConfig(t *testing.T) {
	in := goodInfo("x")
	if _, err := ValidateConfig(in, Config{}); err == nil {
		t.Error("missing required field should fail")
	}
	if _, err := ValidateConfig(in, Config{"base_url": "ftp://x"}); err == nil {
		t.Error("non-http URL should fail")
	}
	if _, err := ValidateConfig(in, Config{"base_url": "https://x", "limit": "lots"}); err == nil {
		t.Error("non-number should fail")
	}
	cfg, err := ValidateConfig(in, Config{"base_url": " https://rock.example.org "})
	if err != nil {
		t.Fatal(err)
	}
	if cfg["limit"] != "20" || cfg["base_url"] != "https://rock.example.org" {
		t.Fatalf("defaults/trim not applied: %v", cfg)
	}
	hosts := AllowedHosts(in, Config{"base_url": "https://Rock.Example.org/api", ExtraHostsKey: "cdn.example.org, "})
	if strings.Join(hosts, ",") != "cdn.example.org,rock.example.org" {
		t.Fatalf("hosts = %v", hosts)
	}
}

func TestHTTPClientHostGuard(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secret"))
	}))
	defer other.Close()
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			// Redirect to a different host name for the same machine.
			u, _ := url.Parse(other.URL)
			http.Redirect(w, r, "http://localhost:"+u.Port()+"/", http.StatusFound)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer allowed.Close()

	c := NewHTTPClient([]string{"127.0.0.1"}, 0, nil)
	ctx := context.Background()
	get := func(u string) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(allowed.URL); err != nil {
		t.Fatalf("allowed host: %v", err)
	}
	if err := get(strings.Replace(other.URL, "127.0.0.1", "localhost", 1)); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("other host should be blocked, got %v", err)
	}
	if err := get(allowed.URL + "/redirect"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("redirect to other host should be blocked, got %v", err)
	}
}

func TestReadLimited(t *testing.T) {
	if _, err := ReadLimited(strings.NewReader("12345"), 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if b, err := ReadLimited(strings.NewReader("1234"), 4); err != nil || string(b) != "1234" {
		t.Fatalf("got %q %v", b, err)
	}
}
