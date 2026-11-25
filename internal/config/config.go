// Package config reads btpolld's TOML file: where to push, and the devices,
// grouped by how they are read.
//
//	push = ["http://metrics.example:8428"]
//
//	[poll]                     # connect, read, disconnect
//	interval = "15m"
//	  [[poll.device]]
//	  name = "den"
//	  type = "wave-plus"
//	  mac  = "00:11:22:33:44:55"
//
//	[broadcast]                # listen to what the device advertises
//	interval = "1m"
//	  [[broadcast.device]]
//	  name = "probe"
//	  type = "switchbot-thermometer"
//	  mac  = "00:11:22:33:44:66"
//
// Errors never repeat a value from the file: push URLs may hold a password
// and addresses name devices in someone's home.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/hughobrien/btpolld/internal/poll"
)

// Device types, and the section each belongs in.
const (
	WavePlus    = "wave-plus"             // under [poll]
	Thermometer = "switchbot-thermometer" // under [broadcast]
)

// Config is a validated file.
type Config struct {
	Push []string // remote write base URLs
	Job  string

	Waves        []poll.Device
	Thermometers []poll.Device
	// WaveInterval and ThermoInterval are the section defaults; zero if the
	// file gave none, which poll replaces with its own.
	WaveInterval   time.Duration
	ThermoInterval time.Duration
}

type file struct {
	Push      []string `toml:"push"`
	Job       string   `toml:"job"`
	Poll      section  `toml:"poll"`
	Broadcast section  `toml:"broadcast"`
}

type section struct {
	Interval string   `toml:"interval"`
	Device   []device `toml:"device"`
}

type device struct {
	Name     string `toml:"name"`
	Type     string `toml:"type"`
	MAC      string `toml:"mac"`
	Interval string `toml:"interval"`
}

// Load reads and validates the file at path.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(b)
}

// Parse validates TOML text.
func Parse(b []byte) (Config, error) {
	var f file
	md, err := toml.Decode(string(b), &f)
	if err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			return Config{}, fmt.Errorf("config: line %d is not valid TOML", pe.Position.Line)
		}
		return Config{}, errors.New("config: not valid TOML")
	}
	if u := md.Undecoded(); len(u) > 0 {
		return Config{}, fmt.Errorf("config: unknown key %s", u[0])
	}

	c := Config{Job: f.Job}
	if c.Job == "" {
		c.Job = "btpolld"
	}
	if len(f.Push) == 0 {
		return Config{}, errors.New("config: push needs at least one URL")
	}
	for i, p := range f.Push {
		u, err := url.Parse(p)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Config{}, fmt.Errorf("config: push[%d] must be an http:// or https:// URL", i)
		}
		c.Push = append(c.Push, p)
	}

	if c.WaveInterval, err = duration("poll.interval", f.Poll.Interval); err != nil {
		return Config{}, err
	}
	if c.ThermoInterval, err = duration("broadcast.interval", f.Broadcast.Interval); err != nil {
		return Config{}, err
	}
	names, macs := map[string]bool{}, map[string]bool{}
	read := func(sec string, ds []device, want string) ([]poll.Device, error) {
		var out []poll.Device
		for i, d := range ds {
			at := fmt.Sprintf("%s.device[%d]", sec, i)
			switch {
			case d.Name == "":
				return nil, fmt.Errorf("config: %s needs a name", at)
			case d.Type != want:
				return nil, fmt.Errorf("config: %s: type must be %q in [%s]", at, want, sec)
			case names[d.Name]:
				return nil, fmt.Errorf("config: %s: the name is used twice", at)
			}
			hw, err := net.ParseMAC(d.MAC)
			if err != nil || len(hw) != 6 {
				return nil, fmt.Errorf("config: %s: mac must look like 00:11:22:33:44:55", at)
			}
			mac := strings.ToUpper(hw.String())
			if macs[mac] {
				return nil, fmt.Errorf("config: %s: the mac is used twice", at)
			}
			iv, err := duration(at+".interval", d.Interval)
			if err != nil {
				return nil, err
			}
			names[d.Name], macs[mac] = true, true
			out = append(out, poll.Device{Name: d.Name, MAC: mac, Interval: iv})
		}
		return out, nil
	}
	if c.Waves, err = read("poll", f.Poll.Device, WavePlus); err != nil {
		return Config{}, err
	}
	if c.Thermometers, err = read("broadcast", f.Broadcast.Device, Thermometer); err != nil {
		return Config{}, err
	}
	if len(c.Waves)+len(c.Thermometers) == 0 {
		return Config{}, errors.New("config: no devices")
	}
	return c, nil
}

// duration reads an optional positive duration such as "90s" or "15m".
func duration(key, v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("config: %s must be a positive duration such as \"90s\" or \"15m\"", key)
	}
	return d, nil
}
