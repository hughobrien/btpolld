package wave

import (
	"encoding/hex"
	"testing"

	"github.com/hughobrien/btpolld/internal/metric"
)

func TestParse(t *testing.T) {
	// A real reading, with radon not yet reported.
	raw, _ := hex.DecodeString("01514d7000000000880751c146032e0000001614")
	r, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Humidity != 40.5 || r.Light != 77 || r.Temperature != 19.28 || r.Pressure != 989.78 || r.CO2 != 838 || r.VOC != 46 {
		t.Fatalf("got %+v", r)
	}
	if r.RadonShort == nil || *r.RadonShort != 0 || r.RadonLong == nil || *r.RadonLong != 0 {
		t.Fatalf("radon: %+v", r)
	}
}

func TestParseRadonUnavailable(t *testing.T) {
	raw, _ := hex.DecodeString("01514d70ffffffff880751c146032e0000001614")
	r, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.RadonShort != nil || r.RadonLong != nil {
		t.Fatalf("radon should be absent: %+v", r)
	}
}

func TestParseErrors(t *testing.T) {
	for name, b := range map[string][]byte{"short": make([]byte, 19), "version": append([]byte{2}, make([]byte, 19)...)} {
		if _, err := Parse(b); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestMetricsRSSI(t *testing.T) {
	has := func(ms []metric.Metric) bool {
		for _, m := range ms {
			if m.Name == "airthings_rssi_dbm" {
				return true
			}
		}
		return false
	}
	if !has(Reading{}.Metrics(-64)) {
		t.Error("rssi series missing")
	}
	if has(Reading{}.Metrics(0)) {
		t.Error("rssi series present without a signal reading")
	}
}
