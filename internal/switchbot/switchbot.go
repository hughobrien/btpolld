// Package switchbot decodes the manufacturer data that a SwitchBot
// thermometer broadcasts, so it can be read without connecting to it.
package switchbot

import (
	"fmt"
	"math"

	"github.com/hughobrien/btpolld/internal/metric"
)

// Prefix starts the name of every series a thermometer reports
// (switchbot_temperature_celsius and so on); a device may be given another.
const Prefix = "switchbot"

// CompanyID is SwitchBot's Bluetooth SIG company identifier, as it appears in
// the manufacturer data of the device's advertisements.
const CompanyID = 0x0969

// ServiceUUID is the service whose data carries the battery level.
const ServiceUUID = "0000fd3d-0000-1000-8000-00805f9b34fb"

// Reading is the temperature and humidity in one advertisement.
type Reading struct {
	Temperature float64 // degrees Celsius
	Humidity    float64 // % relative humidity
}

// ParseThermometer decodes the manufacturer data (the bytes after the company
// identifier) of a SwitchBot thermometer: the device's six address bytes, two
// status bytes, then the temperature's tenths (low four bits), its whole
// degrees (low seven bits of the next byte, whose top bit is set when the
// temperature is not below zero) and the humidity (low seven bits). The
// layout is the one pySwitchbot documents for the Meter, checked here against
// the vendor's app.
func ParseThermometer(b []byte) (Reading, error) {
	if len(b) < 11 {
		return Reading{}, fmt.Errorf("thermometer data is %d bytes, want at least 11", len(b))
	}
	t := float64(b[9]&0x7f) + float64(b[8]&0x0f)/10
	if b[9]&0x80 == 0 {
		t = -t
	}
	return Reading{Temperature: t, Humidity: float64(b[10] & 0x7f)}, nil
}

// The Magnus constants (valid from about -45 to 60 degrees Celsius).
const (
	magnusA = 17.27
	magnusB = 237.3 // degrees Celsius
)

// SaturationPressure is the saturation vapour pressure of water at the
// reading's temperature, in kPa.
func (r Reading) SaturationPressure() float64 {
	return 0.6108 * math.Exp(magnusA*r.Temperature/(r.Temperature+magnusB))
}

// DewPoint is the temperature at which the air's water vapour would condense,
// in degrees Celsius; the Magnus approximation. Zero humidity has no dew
// point, so it reports NaN.
func (r Reading) DewPoint() float64 {
	if r.Humidity <= 0 {
		return math.NaN()
	}
	g := math.Log(r.Humidity/100) + magnusA*r.Temperature/(r.Temperature+magnusB)
	return magnusB * g / (magnusA - g)
}

// AbsoluteHumidity is the mass of water vapour per cubic metre of air, g/m3.
func (r Reading) AbsoluteHumidity() float64 {
	e := r.Humidity / 100 * r.SaturationPressure() * 10 // hPa
	return 216.7 * e / (r.Temperature + 273.15)
}

// VaporPressureDeficit is how much more vapour the air could hold, in kPa.
func (r Reading) VaporPressureDeficit() float64 {
	return r.SaturationPressure() * (1 - r.Humidity/100)
}

// Battery decodes the service data (the bytes under ServiceUUID): a type byte,
// a status byte, then the battery percentage in the low seven bits. ok is
// false if the data is too short or the level is above 100.
func Battery(b []byte) (percent uint8, ok bool) {
	if len(b) < 3 || b[2]&0x7f > 100 {
		return 0, false
	}
	return b[2] & 0x7f, true
}

// Metrics are the series a reading becomes, with the battery percentage and
// the signal strength (dBm) the advertisement arrived with. The battery is
// left out when nil (not every advertisement carries it).
func (r Reading) Metrics(battery *uint8, rssi int16) []metric.Metric {
	m := []metric.Metric{
		{Name: "switchbot_temperature_celsius", Value: r.Temperature},
		{Name: "switchbot_humidity_percent", Value: r.Humidity},
		{Name: "switchbot_absolute_humidity_g_m3", Value: r.AbsoluteHumidity()},
		{Name: "switchbot_vapor_pressure_deficit_kpa", Value: r.VaporPressureDeficit()},
		{Name: "switchbot_rssi_dbm", Value: float64(rssi)},
	}
	if d := r.DewPoint(); !math.IsNaN(d) {
		m = append(m, metric.Metric{Name: "switchbot_dew_point_celsius", Value: d})
	}
	if battery != nil {
		m = append(m, metric.Metric{Name: "switchbot_battery_percent", Value: float64(*battery)})
	}
	return m
}
