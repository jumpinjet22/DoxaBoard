package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Metrics reads link and thermal health from sysfs. Missing values are
// skipped, so this is safe on any Linux box.
func Metrics(iface string) map[string]float64 {
	m := map[string]float64{}
	net := filepath.Join("/sys/class/net", iface)
	readNum(m, "link_speed_mbps", filepath.Join(net, "speed"), 1)
	readNum(m, "rx_errors", filepath.Join(net, "statistics", "rx_errors"), 1)
	readNum(m, "rx_dropped", filepath.Join(net, "statistics", "rx_dropped"), 1)
	readNum(m, "tx_errors", filepath.Join(net, "statistics", "tx_errors"), 1)
	readNum(m, "cpu_temp_c", "/sys/class/thermal/thermal_zone0/temp", 1000)
	if b, err := os.ReadFile(filepath.Join(net, "operstate")); err == nil {
		if strings.TrimSpace(string(b)) == "up" {
			m["link_up"] = 1
		} else {
			m["link_up"] = 0
		}
	}
	return m
}

func readNum(m map[string]float64, key, path string, div float64) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil || v < 0 { // speed reads -1 when the link is down
		return
	}
	m[key] = v / div
}
