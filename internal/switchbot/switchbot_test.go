package switchbot

import (
	"encoding/hex"
	"math"
	"testing"
)

func TestParseThermometer(t *testing.T) {
	// Real advertisements (the address bytes are invented).
	for _, c := range []struct {
		hex  string
		temp float64
		hum  float64
	}{
		{"aabbccddeeff100205992000", 25.5, 32},
		{"aabbccddeeff1b0203962400", 22.3, 36},
		{"aabbccddeeff1b0209022400", -2.9, 36}, // below zero: sign bit clear
	} {
		b, _ := hex.DecodeString(c.hex)
		r, err := ParseThermometer(b)
		if err != nil {
			t.Fatal(err)
		}
		if r.Temperature != c.temp || r.Humidity != c.hum {
			t.Errorf("%s: got %+v, want %v %v", c.hex, r, c.temp, c.hum)
		}
	}
}

func TestParseThermometerErrors(t *testing.T) {
	if _, err := ParseThermometer(make([]byte, 10)); err == nil {
		t.Error("short data accepted")
	}
}

// The vendor's app showed these for 23.1 C and 38 %.
func TestDerived(t *testing.T) {
	r := Reading{Temperature: 23.1, Humidity: 38}
	for name, c := range map[string]struct{ got, want float64 }{
		"absolute humidity": {r.AbsoluteHumidity(), 7.86},
		"dew point":         {r.DewPoint(), 8.0},
		"vpd":               {r.VaporPressureDeficit(), 1.75},
	} {
		if math.Abs(c.got-c.want) > 0.05 {
			t.Errorf("%s: got %.3f, want %.2f", name, c.got, c.want)
		}
	}
	if !math.IsNaN((Reading{Temperature: 5}).DewPoint()) {
		t.Error("dew point at 0% should be NaN")
	}
}

func TestBattery(t *testing.T) {
	if p, ok := Battery([]byte{0x77, 0x00, 0x64}); !ok || p != 100 {
		t.Errorf("got %d %v", p, ok)
	}
	if p, ok := Battery([]byte{0x77, 0x00, 0xd5}); !ok || p != 85 {
		t.Errorf("got %d %v", p, ok)
	}
	if _, ok := Battery([]byte{0x77, 0x00}); ok {
		t.Error("short data accepted")
	}
	if _, ok := Battery([]byte{0x77, 0x00, 0x7f}); ok {
		t.Error("127 % accepted")
	}
}
