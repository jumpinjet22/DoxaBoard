package all

import (
	"testing"

	"github.com/jumpinjet22/doxaboard/connectors/registry"
)

func TestAllRegistered(t *testing.T) {
	for _, name := range []string{"jsonfeed", "rss", "folder"} {
		c, ok := registry.Get(name)
		if !ok {
			t.Errorf("connector %q is not registered", name)
			continue
		}
		info := c.Info()
		if info.Name != name || info.Status != registry.StatusOfficial || info.Maintainer != "@jumpinjet22" {
			t.Errorf("%s: unexpected info %+v", name, info)
		}
		if err := registry.CheckInfo(info); err != nil {
			t.Error(err)
		}
	}
	if c, ok := registry.Get("rock"); !ok {
		t.Error(`connector "rock" is not registered`)
	} else if err := registry.CheckInfo(c.Info()); err != nil {
		t.Error(err)
	}
	if c, _ := registry.Get("folder"); c != nil && !c.Info().UsesFiles {
		t.Error("folder should set UsesFiles")
	}
}
