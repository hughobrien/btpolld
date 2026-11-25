package poll

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/btpolld/internal/bluez"
	"github.com/hughobrien/btpolld/internal/promwrite"
)

const goodRaw = "01514d7000000000880751c146032e0000001614" // radon 0/0
const noRadonRaw = "01514d70ffffffff880751c146032e0000001614"

type fakeReader struct {
	mu     sync.Mutex
	calls  []string
	script []func() ([]byte, error)
	rssi   int16
}

func (f *fakeReader) Read(_ context.Context, mac, uuid string) ([]byte, int16, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, mac+" "+uuid)
	i := min(len(f.calls)-1, len(f.script)-1)
	b, err := f.script[i]()
	return b, f.rssi, err
}

func ok(h string) func() ([]byte, error) {
	return func() ([]byte, error) { b, _ := hex.DecodeString(h); return b, nil }
}

func fail() ([]byte, error) { return nil, errors.New("not available") }

// sink records what is added and counts flushes.
type sink struct {
	mu      sync.Mutex
	series  []promwrite.Series
	flushes int
}

func (s *sink) Add(series ...promwrite.Series) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.series = append(s.series, series...)
}

func (s *sink) Flush(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushes++
	return nil
}

// values returns name -> value of the series added for a device.
func (s *sink) values(device string) map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]float64{}
	for _, se := range s.series {
		if se.Labels["device"] == device {
			out[se.Labels["__name__"]] = se.Samples[0].V
		}
	}
	return out
}

func newPoller(rd Reader, ln Listener, s *sink, cfg Config) *Poller {
	cfg.Job = "btpolld"
	p := New(cfg, rd, ln, s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.sleep = func(context.Context, time.Duration) {}
	return p
}

var den = []Device{{Name: "den", MAC: "00:11:22:33:44:55"}}

func TestPollWaveAddsReading(t *testing.T) {
	rd := &fakeReader{script: []func() ([]byte, error){ok(goodRaw)}}
	s := &sink{}
	p := newPoller(rd, nil, s, Config{Waves: den})
	p.PollWaves(context.Background())
	v := s.values("den")
	for name, want := range map[string]float64{
		"airthings_co2_ppm": 838, "airthings_temperature_celsius": 19.28, "airthings_humidity_percent": 40.5,
		"airthings_pressure_hpa": 989.78, "airthings_radon_short_term_bq_m3": 0,
	} {
		if v[name] != want {
			t.Errorf("%s = %v, want %v", name, v[name], want)
		}
	}
	if s.flushes != 1 {
		t.Errorf("flushes = %d", s.flushes)
	}
	if got := rd.calls[0]; got != "00:11:22:33:44:55 b42e2a68-ade7-11e4-89d3-123b93f75cba" {
		t.Errorf("read %q", got)
	}
	if s.series[0].Labels["job"] != "btpolld" {
		t.Errorf("labels %v", s.series[0].Labels)
	}
}

func TestRadonOmittedWhenUnavailable(t *testing.T) {
	s := &sink{}
	newPoller(&fakeReader{script: []func() ([]byte, error){ok(noRadonRaw)}}, nil, s, Config{Waves: den}).PollWaves(context.Background())
	v := s.values("den")
	if _, has := v["airthings_radon_short_term_bq_m3"]; has || v["airthings_co2_ppm"] != 838 {
		t.Fatalf("%v", v)
	}
}

func TestRetriesThenSucceeds(t *testing.T) {
	rd := &fakeReader{script: []func() ([]byte, error){fail, fail, ok(goodRaw)}}
	s := &sink{}
	newPoller(rd, nil, s, Config{Waves: den}).PollWaves(context.Background())
	if len(rd.calls) != 3 || s.values("den")["airthings_co2_ppm"] != 838 {
		t.Fatalf("calls %d", len(rd.calls))
	}
}

func TestFailureAddsNothing(t *testing.T) {
	rd := &fakeReader{script: []func() ([]byte, error){fail}}
	s := &sink{}
	newPoller(rd, nil, s, Config{Waves: den}).PollWaves(context.Background())
	if len(rd.calls) != 3 || len(s.series) != 0 {
		t.Fatalf("calls %d, series %d", len(rd.calls), len(s.series))
	}
}

func TestBadPayloadAddsNothing(t *testing.T) {
	s := &sink{}
	newPoller(&fakeReader{script: []func() ([]byte, error){ok("0102")}}, nil, s, Config{Waves: den}).PollWaves(context.Background())
	if len(s.series) != 0 {
		t.Fatal("a bad payload was pushed")
	}
}

type fakeListener struct {
	mu      sync.Mutex
	adverts []bluez.Advert
	fail    error // returned once, before any advert
	calls   int
}

func (f *fakeListener) Listen(ctx context.Context, mac string, company uint16, serviceUUID string, fn func(bluez.Advert)) error {
	f.mu.Lock()
	f.calls++
	fail := f.fail
	f.fail = nil
	f.mu.Unlock()
	if fail != nil {
		return fail
	}
	for _, a := range f.adverts {
		fn(a)
	}
	<-ctx.Done()
	return ctx.Err()
}

func mkAdvert(t *testing.T, h string, rssi int16) bluez.Advert {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	return bluez.Advert{Data: b, RSSI: rssi}
}

var probe = []Device{{Name: "probe", MAC: "00:11:22:33:44:66"}}

func TestThermometerSample(t *testing.T) {
	s := &sink{}
	p := newPoller(nil, nil, s, Config{Thermometers: probe})
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }
	a := mkAdvert(t, "aabbccddeeff1b0203962600", -90) // 22.3 C, 38 % (0x26)
	a.Service = []byte{0x77, 0, 0xd5}
	p.hear(probe[0], a)
	p.sampleThermometers()
	v := s.values("probe")
	for name, want := range map[string]float64{
		"switchbot_temperature_celsius": 22.3, "switchbot_humidity_percent": 38, "switchbot_rssi_dbm": -90, "switchbot_battery_percent": 85,
	} {
		if v[name] != want {
			t.Errorf("%s = %v, want %v", name, v[name], want)
		}
	}
	for _, name := range []string{"switchbot_dew_point_celsius", "switchbot_absolute_humidity_g_m3", "switchbot_vapor_pressure_deficit_kpa"} {
		if _, has := v[name]; !has {
			t.Errorf("missing %s", name)
		}
	}
	// Unheard for longer than the stale limit: nothing is added.
	n := len(s.series)
	now = now.Add(4 * time.Minute)
	p.sampleThermometers()
	if len(s.series) != n {
		t.Error("a stale reading was pushed")
	}
}

func TestThermometerBatteryAbsentWithoutServiceData(t *testing.T) {
	s := &sink{}
	p := newPoller(nil, nil, s, Config{Thermometers: probe})
	p.hear(probe[0], mkAdvert(t, "aabbccddeeff1b0203962600", -90))
	p.sampleThermometers()
	if _, has := s.values("probe")["switchbot_battery_percent"]; has {
		t.Error("battery pushed without service data")
	}
}

func TestBadAdvertIgnored(t *testing.T) {
	s := &sink{}
	p := newPoller(nil, nil, s, Config{Thermometers: probe})
	p.hear(probe[0], bluez.Advert{Data: []byte{1, 2}})
	p.sampleThermometers()
	if len(s.series) != 0 {
		t.Fatal("a bad advertisement was pushed")
	}
}

func TestRunListensRestartsAndSamples(t *testing.T) {
	ln := &fakeListener{fail: errors.New("no adapter"), adverts: []bluez.Advert{mkAdvert(t, "aabbccddeeff1b0203962600", -80)}}
	s := &sink{}
	p := newPoller(nil, ln, s, Config{Thermometers: probe, ThermoInterval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for s.values("probe")["switchbot_temperature_celsius"] != 22.3 {
		if time.Now().After(deadline) {
			t.Fatal("no sample after the listener failed once")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if ln.calls != 2 {
		t.Fatalf("listen calls %d", ln.calls)
	}
}

func TestWaveRSSIPrefersFreshAdvert(t *testing.T) {
	d := Device{Name: "w", MAC: "aa"}
	p := newPoller(nil, &fakeListener{}, &sink{}, Config{Waves: []Device{d}})
	if got := p.waveRSSI(d, -70); got != -70 {
		t.Errorf("no advert: got %d, want the read's -70", got)
	}
	p.waveSig["w"] = &advert{at: p.now(), rssi: -60}
	if got := p.waveRSSI(d, -70); got != -60 {
		t.Errorf("fresh advert: got %d, want -60", got)
	}
	p.waveSig["w"].at = p.now().Add(-time.Hour)
	if got := p.waveRSSI(d, -70); got != -70 {
		t.Errorf("stale advert: got %d, want the read's -70", got)
	}
}

func count(s *sink, device, name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, x := range s.series {
		if x.Labels["device"] == device && x.Labels["__name__"] == name {
			n += len(x.Samples)
		}
	}
	return n
}

func TestBroadcastRecordsChangesNotRepeats(t *testing.T) {
	s := &sink{}
	p := newPoller(nil, nil, s, Config{Thermometers: probe})
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }
	const temp = "switchbot_temperature_celsius"
	same := mkAdvert(t, "aabbccddeeff1b0203962600", -90) // 22.3 C, 38 %
	p.hear(probe[0], same)
	for range 5 { // the same values again, with a wobbling signal
		now = now.Add(2 * time.Second)
		a := same
		a.RSSI -= 3
		p.hear(probe[0], a)
	}
	if n := count(s, "probe", temp); n != 1 {
		t.Fatalf("identical advertisements recorded %d times, want 1", n)
	}
	now = now.Add(2 * time.Second)
	p.hear(probe[0], mkAdvert(t, "aabbccddeeff1b0203972600", -90)) // 23.3 C
	if n := count(s, "probe", temp); n != 2 {
		t.Fatalf("a changed reading was not recorded: %d samples", n)
	}
	// The sample carries the time it was heard.
	last := s.series[len(s.series)-1]
	if !last.Samples[0].T.Equal(now) {
		t.Errorf("sample time %v, want %v", last.Samples[0].T, now)
	}
}

func TestBroadcastRepeatsSteadyReading(t *testing.T) {
	s := &sink{}
	p := newPoller(nil, nil, s, Config{Thermometers: []Device{{Name: "probe", MAC: "00:11:22:33:44:66", Interval: 30 * time.Second}}})
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }
	const temp = "switchbot_temperature_celsius"
	p.hear(probe[0], mkAdvert(t, "aabbccddeeff1b0203962600", -90))
	p.sampleThermometers()
	if n := count(s, "probe", temp); n != 1 {
		t.Fatalf("repeat too soon: %d samples", n)
	}
	now = now.Add(31 * time.Second) // device interval, not the 1m default
	p.sampleThermometers()
	if n := count(s, "probe", temp); n != 2 {
		t.Fatalf("steady reading not repeated: %d samples", n)
	}
	now = now.Add(2 * time.Minute) // unheard for over three intervals
	p.sampleThermometers()
	if n := count(s, "probe", temp); n != 2 {
		t.Fatalf("a stale reading was repeated: %d samples", n)
	}
}

func TestBroadcastKeepsLastBattery(t *testing.T) {
	s := &sink{}
	p := newPoller(nil, nil, s, Config{Thermometers: probe})
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }
	a := mkAdvert(t, "aabbccddeeff1b0203962600", -90)
	a.Service = []byte{0x77, 0, 0xd5}
	p.hear(probe[0], a)
	now = now.Add(time.Second)
	p.hear(probe[0], mkAdvert(t, "aabbccddeeff1b0203972600", -90)) // no service data this time
	if got := s.values("probe")["switchbot_battery_percent"]; got != 85 {
		t.Errorf("battery %v, want the last known 85", got)
	}
}

func TestPrefixRenamesSeries(t *testing.T) {
	s := &sink{}
	d := []Device{{Name: "den", MAC: "00:11:22:33:44:55", Prefix: "wave"}}
	newPoller(&fakeReader{script: []func() ([]byte, error){ok(goodRaw)}}, nil, s, Config{Waves: d}).PollWaves(context.Background())
	v := s.values("den")
	if v["wave_co2_ppm"] != 838 {
		t.Errorf("renamed series missing: %v", v)
	}
	if _, has := v["airthings_co2_ppm"]; has {
		t.Error("old name still pushed")
	}

	s = &sink{}
	pr := []Device{{Name: "probe", MAC: "00:11:22:33:44:66", Prefix: "fridge"}}
	p := newPoller(nil, nil, s, Config{Thermometers: pr})
	p.hear(pr[0], mkAdvert(t, "aabbccddeeff1b0203962600", -90))
	if s.values("probe")["fridge_temperature_celsius"] != 22.3 {
		t.Errorf("renamed thermometer series missing: %v", s.values("probe"))
	}
}
