package config

import (
	"strings"
	"testing"
	"time"
)

const good = `
push = ["http://metrics.example:8428"]
job = "home"

[poll]
interval = "10m"
  [[poll.device]]
  name = "den"
  type = "wave-plus"
  mac = "aa:bb:cc:00:11:01"
  [[poll.device]]
  name = "loft"
  type = "wave-plus"
  mac = "aa:bb:cc:00:11:02"
  interval = "5m"

[broadcast]
interval = "30s"
  [[broadcast.device]]
  name = "probe"
  type = "switchbot-thermometer"
  mac = "AA:BB:CC:00:11:03"
`

func TestParse(t *testing.T) {
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if c.Job != "home" || len(c.Push) != 1 || len(c.Waves) != 2 || len(c.Thermometers) != 1 {
		t.Fatalf("%+v", c)
	}
	if c.WaveInterval != 10*time.Minute || c.ThermoInterval != 30*time.Second {
		t.Errorf("section intervals %v %v", c.WaveInterval, c.ThermoInterval)
	}
	if c.Waves[0].Interval != 0 || c.Waves[1].Interval != 5*time.Minute {
		t.Errorf("device intervals %v %v", c.Waves[0].Interval, c.Waves[1].Interval)
	}
	if c.Waves[0].MAC != "AA:BB:CC:00:11:01" {
		t.Errorf("mac %q", c.Waves[0].MAC)
	}
}

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte(`push = ["http://a:1"]
[[broadcast.device]]
name = "p"
type = "switchbot-thermometer"
mac = "aa:bb:cc:00:11:03"`))
	if err != nil || c.Job != "btpolld" || c.ThermoInterval != 0 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestInvalid(t *testing.T) {
	dev := func(sec, typ, name, mac string) string {
		return "[[" + sec + ".device]]\nname = \"" + name + "\"\ntype = \"" + typ + "\"\nmac = \"" + mac + "\"\n"
	}
	push := `push = ["http://a:1"]` + "\n"
	cases := map[string]string{
		"no push":           dev("poll", "wave-plus", "a", "aa:bb:cc:00:11:01"),
		"no devices":        push,
		"bad push scheme":   `push = ["ftp://a"]` + "\n" + dev("poll", "wave-plus", "a", "aa:bb:cc:00:11:01"),
		"wave in broadcast": push + dev("broadcast", "wave-plus", "a", "aa:bb:cc:00:11:01"),
		"probe in poll":     push + dev("poll", "switchbot-thermometer", "a", "aa:bb:cc:00:11:01"),
		"bad mac":           push + dev("poll", "wave-plus", "a", "nonsense"),
		"no name":           push + dev("poll", "wave-plus", "", "aa:bb:cc:00:11:01"),
		"duplicate name":    push + dev("poll", "wave-plus", "a", "aa:bb:cc:00:11:01") + dev("poll", "wave-plus", "a", "aa:bb:cc:00:11:02"),
		"duplicate mac":     push + dev("poll", "wave-plus", "a", "aa:bb:cc:00:11:01") + dev("poll", "wave-plus", "b", "AA-BB-CC-00-11-01"),
		"bad interval":      push + "[poll]\ninterval = \"soon\"\n" + dev("poll", "wave-plus", "a", "aa:bb:cc:00:11:01"),
		"negative interval": push + "[poll]\ninterval = \"-1m\"\n" + dev("poll", "wave-plus", "a", "aa:bb:cc:00:11:01"),
		"unknown key":       push + "colour = 1\n" + dev("poll", "wave-plus", "a", "aa:bb:cc:00:11:01"),
		"not toml":          "push = [",
	}
	for name, text := range cases {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestErrorsHideValues(t *testing.T) {
	for _, text := range []string{
		`push = ["ftp://user:hunter2@host"]` + "\n" + `[[poll.device]]` + "\nname=\"a\"\ntype=\"wave-plus\"\nmac=\"aa:bb:cc:00:11:01\"\n",
		`push = ["http://a:1"]` + "\n" + `[[poll.device]]` + "\nname=\"a\"\ntype=\"wave-plus\"\nmac=\"hunter2\"\n",
		`push = ["http://user:hunter2@a:1"` + "\n",
	} {
		_, err := Parse([]byte(text))
		if err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error %v", err)
		}
	}
}

// The example in deploy/ is what people copy, so it must load.
func TestExampleFileLoads(t *testing.T) {
	c, err := Load("../../deploy/btpolld.toml.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Waves) != 2 || len(c.Thermometers) != 1 || len(c.Push) != 1 {
		t.Fatalf("%+v", c)
	}
}

func TestPrefix(t *testing.T) {
	c, err := Parse([]byte(`push = ["http://a:1"]
[poll]
prefix = "wave"
  [[poll.device]]
  name = "a"
  type = "wave-plus"
  mac = "aa:bb:cc:00:11:01"
  [[poll.device]]
  name = "b"
  type = "wave-plus"
  mac = "aa:bb:cc:00:11:02"
  prefix = "loft"
[[broadcast.device]]
name = "p"
type = "switchbot-thermometer"
mac = "aa:bb:cc:00:11:03"`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Waves[0].Prefix != "wave" || c.Waves[1].Prefix != "loft" || c.Thermometers[0].Prefix != "" {
		t.Errorf("%q %q %q", c.Waves[0].Prefix, c.Waves[1].Prefix, c.Thermometers[0].Prefix)
	}
	for _, bad := range []string{"9lives", "a-b", "a b"} {
		_, err := Parse([]byte(`push = ["http://a:1"]
[[poll.device]]
name = "a"
type = "wave-plus"
mac = "aa:bb:cc:00:11:01"
prefix = "` + bad + `"`))
		if err == nil {
			t.Errorf("prefix %q accepted", bad)
		}
	}
}
