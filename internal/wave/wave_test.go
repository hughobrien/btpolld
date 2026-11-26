package wave

import (
	"encoding/hex"
	"testing"
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

func TestSignalMetrics(t *testing.T) {
	if ms := SignalMetrics(-64); len(ms) != 1 || ms[0].Name != "airthings_rssi_dbm" || ms[0].Value != -64 {
		t.Errorf("signal: %v", ms)
	}
	if ms := SignalMetrics(0); len(ms) != 0 {
		t.Errorf("a zero signal was reported: %v", ms)
	}
	for _, m := range (Reading{}).Metrics() {
		if m.Name == "airthings_rssi_dbm" {
			t.Error("a reading carries no signal strength")
		}
	}
}

func TestHumidityOutOfRange(t *testing.T) {
	b, _ := hex.DecodeString("01c94d7000000000880751c146032e0000001614") // 0xc9 = 201 half percent
	if _, err := Parse(b); err == nil {
		t.Error("humidity above 100 accepted")
	}
}
