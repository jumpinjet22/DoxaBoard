package connectortest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jumpinjet22/doxaboard/connectors/registry"
	"github.com/jumpinjet22/doxaboard/slides"
)

// demo is a minimal, well-behaved connector used to test the kit itself.
type demo struct{}

func (demo) Info() registry.Info {
	return registry.Info{
		Name: "demo", Title: "Demo", Description: "Kit self-test", Status: registry.StatusExperimental,
		Maintainer: "@doxaboard",
		Fields:     []registry.Field{{Key: "base_url", Label: "Base URL", Type: registry.FieldURL, Required: true}},
	}
}

func (demo) New(cfg registry.Config, d registry.Deps) (registry.Source, error) {
	if cfg["base_url"] == "" {
		return nil, fmt.Errorf("base_url required")
	}
	return demoSource{url: cfg["base_url"] + "/items", http: d.HTTP}, nil
}

type demoSource struct {
	url  string
	http *http.Client
}

func (s demoSource) Fetch(ctx context.Context) ([]slides.Slide, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := registry.CheckStatus(resp); err != nil {
		return nil, err
	}
	b, err := registry.ReadLimited(resp.Body, 0)
	if err != nil {
		return nil, err
	}
	var items []struct{ ID, Name string }
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, err
	}
	out := make([]slides.Slide, len(items))
	for i, it := range items {
		out[i] = slides.Slide{ID: it.ID, Title: it.Name}
	}
	return out, nil
}

func TestKitPassesGoodConnector(t *testing.T) {
	Run(t, demo{}, []Fixture{{
		Name:    "basic",
		Config:  registry.Config{"base_url": "{{server}}"},
		Routes:  map[string]Response{"/items": {Body: []byte(`[{"ID":"a","Name":"Alpha"},{"ID":"b","Name":"Beta"}]`)}},
		WantIDs: []string{"a", "b"},
	}})
}
