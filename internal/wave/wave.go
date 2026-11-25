// Package wave decodes the current-values characteristic of an Airthings
// Wave Plus.
package wave

import (
	"encoding/binary"
	"fmt"
	"github.com/hughobrien/btpolld/internal/metric"
)

// Service and characteristic UUIDs of the Wave Plus.
const (
	ServiceUUID = "b42e1c08-ade7-11e4-89d3-123b93f75cba"
	// CurrentValues is the characteristic that holds the latest reading.
	CurrentValues = "b42e2a68-ade7-11e4-89d3-123b93f75cba"
)

// CompanyID is Airthings' Bluetooth SIG company identifier, as it appears in
// the manufacturer data of the device's advertisements.
const CompanyID = 0x0334

// Reading is one set of current values.
type Reading struct {
	Humidity    float64 // % relative humidity
	Light       uint8   // ambient light, unitless, 0 to 255 as reported
	RadonShort  *uint16 // Bq/m3, 24 h average; nil until the device has a value
	RadonLong   *uint16 // Bq/m3, long-term average; nil until the device has a value
	Temperature float64 // degrees Celsius
	Pressure    float64 // hPa
	CO2         uint16  // ppm
	VOC         uint16  // ppb
}

// maxRadon is the largest valid radon value; the device reports others (0xFFFF,
// for one) while it has none yet.
const maxRadon = 16383

// Parse decodes the 20 bytes of the current-values characteristic: version
// (1), humidity in half percent, light, one unused byte, then little-endian
// uint16s: radon short-term, radon long-term, temperature in hundredths of a
// degree, pressure in fiftieths of a hPa, CO2, VOC and two unused.
func Parse(b []byte) (Reading, error) {
	if len(b) < 20 {
		return Reading{}, fmt.Errorf("wave plus reading: %d bytes, want 20", len(b))
	}
	if b[0] != 1 {
		return Reading{}, fmt.Errorf("wave plus reading: unknown version %d", b[0])
	}
	u := func(i int) uint16 { return binary.LittleEndian.Uint16(b[4+2*i:]) }
	r := Reading{
		Humidity:    float64(b[1]) / 2,
		Light:       b[2],
		Temperature: float64(u(2)) / 100,
		Pressure:    float64(u(3)) / 50,
		CO2:         u(4),
		VOC:         u(5),
	}
	if v := u(0); v <= maxRadon {
		r.RadonShort = &v
	}
	if v := u(1); v <= maxRadon {
		r.RadonLong = &v
	}
	return r, nil
}

// Metrics are the series a reading becomes. Radon is left out while the
// device has no value. The names keep the airthings_ prefix of the dashboards
// that came before.
func (r Reading) Metrics(rssi int16) []metric.Metric {
	m := []metric.Metric{
		{Name: "airthings_co2_ppm", Value: float64(r.CO2)},
		{Name: "airthings_voc_ppb", Value: float64(r.VOC)},
		{Name: "airthings_temperature_celsius", Value: r.Temperature},
		{Name: "airthings_humidity_percent", Value: r.Humidity},
		{Name: "airthings_pressure_hpa", Value: r.Pressure},
		{Name: "airthings_light", Value: float64(r.Light)},
	}
	if rssi != 0 {
		m = append(m, metric.Metric{Name: "airthings_rssi_dbm", Value: float64(rssi)})
	}
	if r.RadonShort != nil {
		m = append(m, metric.Metric{Name: "airthings_radon_short_term_bq_m3", Value: float64(*r.RadonShort)})
	}
	if r.RadonLong != nil {
		m = append(m, metric.Metric{Name: "airthings_radon_long_term_bq_m3", Value: float64(*r.RadonLong)})
	}
	return m
}
