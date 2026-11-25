package poll

import (
	"os"
	"regexp"
	"testing"

	"github.com/hughobrien/btpolld/internal/switchbot"
	"github.com/hughobrien/btpolld/internal/wave"
)

// The importable dashboard must only use metrics btpolld pushes.
func TestDashboardUsesPushedMetrics(t *testing.T) {
	b, err := os.ReadFile("../../grafana/btpolld.json")
	if err != nil {
		t.Fatal(err)
	}
	one := uint16(1)
	bat := uint8(1)
	pushed := map[string]bool{}
	for _, m := range (wave.Reading{RadonShort: &one, RadonLong: &one}).Metrics() {
		pushed[m.Name] = true
	}
	for _, m := range (switchbot.Reading{Temperature: 20, Humidity: 50}).Metrics(&bat, -70) {
		pushed[m.Name] = true
	}
	used := regexp.MustCompile(`(airthings|switchbot)_[a-z0-9_]+`).FindAllString(string(b), -1)
	if len(used) == 0 {
		t.Fatal("dashboard uses no airthings metric")
	}
	for _, name := range used {
		if !pushed[name] {
			t.Errorf("dashboard uses %s, which btpolld does not push", name)
		}
	}
}
