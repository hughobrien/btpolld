// Package poll gets readings from the Bluetooth LE devices and pushes each
// one, at the time it was taken, to a Sink.
//
// An Airthings Wave Plus is polled: every WaveInterval the poller connects, reads
// its current values and disconnects. A SwitchBot thermometer is listened to:
// it broadcasts its values, so every ThermoInterval the newest advertisement
// is recorded, if one was heard recently enough.
package poll

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hughobrien/btpolld/internal/bluez"
	"github.com/hughobrien/btpolld/internal/metric"
	"github.com/hughobrien/btpolld/internal/promwrite"
	"github.com/hughobrien/btpolld/internal/switchbot"
	"github.com/hughobrien/btpolld/internal/wave"
)

// Reader reads a GATT characteristic from the device with the given address.
type Reader interface {
	Read(ctx context.Context, mac, uuid string) (val []byte, rssi int16, err error)
}

// Listener calls fn each time the device at mac advertises, with the
// manufacturer data of company and the service data under serviceUUID, until
// ctx ends or listening fails.
type Listener interface {
	Listen(ctx context.Context, mac string, company uint16, serviceUUID string, fn func(bluez.Advert)) error
}

// Sink takes the series to push, and sends what it has been given.
type Sink interface {
	Add(series ...promwrite.Series)
	Flush(ctx context.Context) error
}

// Device is one sensor.
type Device struct {
	Name string // the device label; defaults to the address
	MAC  string
}

// Config holds the poller's settings. Zero durations and Attempts take the
// default in brackets.
type Config struct {
	Job          string // job label on pushed series
	Waves        []Device
	Thermometers []Device

	WaveInterval   time.Duration // how often to read each Wave Plus (15m)
	ThermoInterval time.Duration // how often to record each thermometer (1m)
	ThermoStale    time.Duration // a thermometer unheard for this long is down (three intervals)
	Attempts       int           // reads tried per Wave Plus per poll (3)
	Timeout        time.Duration // bound on one read, scanning and connecting included (45s)
	RetryDelay     time.Duration // pause between attempts, and before listening again (5s)
	FlushTimeout   time.Duration // bound on one write to a target (10s)
}

// Poller polls and listens.
type Poller struct {
	cfg   Config
	rd    Reader
	ln    Listener
	sink  Sink
	log   *slog.Logger
	now   func() time.Time
	sleep func(context.Context, time.Duration)

	flushMu sync.Mutex // one flush at a time
	mu      sync.Mutex
	heard   map[string]*advert // newest advertisement, by thermometer name
	waveSig map[string]*advert // newest advertisement of each Wave Plus, by name; only rssi and at are set
}

// waveSigFresh is how recent an advertisement must be for its signal
// strength to stand for a Wave Plus read: a read does not scan, and BlueZ
// forgets a device's RSSI soon after it stops advertising.
const waveSigFresh = 10 * time.Minute

// advert is what the newest advertisement of a thermometer said.
type advert struct {
	at      time.Time
	reading switchbot.Reading
	battery *uint8
	rssi    int16
}

// New returns a Poller; call Run to start it.
func New(cfg Config, rd Reader, ln Listener, sink Sink, log *slog.Logger) *Poller {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&cfg.WaveInterval, 15*time.Minute)
	def(&cfg.ThermoInterval, time.Minute)
	def(&cfg.ThermoStale, 3*cfg.ThermoInterval)
	def(&cfg.Timeout, 45*time.Second)
	def(&cfg.RetryDelay, 5*time.Second)
	def(&cfg.FlushTimeout, 10*time.Second)
	if cfg.Attempts <= 0 {
		cfg.Attempts = 3
	}
	name := func(ds []Device) []Device {
		ds = append([]Device(nil), ds...)
		for i := range ds {
			if ds[i].Name == "" {
				ds[i].Name = ds[i].MAC
			}
		}
		return ds
	}
	cfg.Waves, cfg.Thermometers = name(cfg.Waves), name(cfg.Thermometers)
	return &Poller{cfg: cfg, rd: rd, ln: ln, sink: sink, log: log, now: time.Now, sleep: sleepCtx, heard: map[string]*advert{}, waveSig: map[string]*advert{}}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Run polls and listens until ctx ends, then flushes what is queued once
// more.
func (p *Poller) Run(ctx context.Context) {
	var wg sync.WaitGroup
	if len(p.cfg.Waves) > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); p.runWaves(ctx) }()
	}
	if len(p.cfg.Thermometers) > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); p.runThermometers(ctx) }()
	}
	wg.Wait()
	fctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.flush(fctx)
}

func (p *Poller) runWaves(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for _, d := range p.cfg.Waves {
		wg.Add(1)
		go func() { defer wg.Done(); p.listenWave(ctx, d) }()
	}
	t := time.NewTicker(p.cfg.WaveInterval)
	defer t.Stop()
	for {
		p.PollWaves(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// PollWaves reads every Wave Plus once (with retries) and flushes.
func (p *Poller) PollWaves(ctx context.Context) {
	for _, d := range p.cfg.Waves {
		if ctx.Err() != nil {
			return
		}
		p.pollWave(ctx, d)
	}
	p.flush(ctx)
}

func (p *Poller) pollWave(ctx context.Context, d Device) {
	var (
		r    wave.Reading
		rssi int16
		err  error
	)
	for attempt := 1; attempt <= p.cfg.Attempts; attempt++ {
		r, rssi, err = p.readWave(ctx, d)
		if err == nil {
			break
		}
		p.log.Warn("read failed", "device", d.Name, "attempt", attempt, "err", err)
		if attempt < p.cfg.Attempts {
			p.sleep(ctx, p.cfg.RetryDelay)
			if ctx.Err() != nil {
				return
			}
		}
	}
	if err != nil {
		return
	}
	p.sink.Add(p.series(d.Name, p.now(), r.Metrics(p.waveRSSI(d, rssi)))...)
	p.log.Info("read", "device", d.Name)
}

func (p *Poller) readWave(ctx context.Context, d Device) (wave.Reading, int16, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	raw, rssi, err := p.rd.Read(ctx, d.MAC, wave.CurrentValues)
	if err != nil {
		return wave.Reading{}, 0, err
	}
	r, err := wave.Parse(raw)
	if err != nil {
		return wave.Reading{}, 0, fmt.Errorf("decode: %w", err)
	}
	return r, rssi, nil
}

func (p *Poller) runThermometers(ctx context.Context) {
	var wg sync.WaitGroup
	for _, d := range p.cfg.Thermometers {
		wg.Add(1)
		go func() { defer wg.Done(); p.listen(ctx, d) }()
	}
	t := time.NewTicker(p.cfg.ThermoInterval)
	defer t.Stop()
	for done := false; !done; {
		select {
		case <-ctx.Done():
			done = true
		case <-t.C:
			p.sampleThermometers()
			p.flush(ctx)
		}
	}
	wg.Wait()
}

// listen keeps a scan for d going, restarting it after a failure.
func (p *Poller) listen(ctx context.Context, d Device) {
	for ctx.Err() == nil {
		err := p.ln.Listen(ctx, d.MAC, switchbot.CompanyID, switchbot.ServiceUUID, func(a bluez.Advert) { p.hear(d, a) })
		if ctx.Err() != nil {
			return
		}
		p.log.Warn("listen failed", "device", d.Name, "err", err)
		p.sleep(ctx, p.cfg.RetryDelay)
	}
}

// listenWave keeps a scan for a Wave Plus going, only to learn its signal
// strength; the values come from reading it.
func (p *Poller) listenWave(ctx context.Context, d Device) {
	for ctx.Err() == nil {
		err := p.ln.Listen(ctx, d.MAC, wave.CompanyID, "", func(a bluez.Advert) {
			if a.RSSI == 0 {
				return
			}
			p.mu.Lock()
			p.waveSig[d.Name] = &advert{at: p.now(), rssi: a.RSSI}
			p.mu.Unlock()
		})
		if ctx.Err() != nil {
			return
		}
		p.log.Warn("listen failed", "device", d.Name, "err", err)
		p.sleep(ctx, p.cfg.RetryDelay)
	}
}

// waveRSSI is the freshest signal strength known for a Wave Plus: what its
// advertisements said, else what the read itself found (zero for none).
func (p *Poller) waveRSSI(d Device, fromRead int16) int16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if a := p.waveSig[d.Name]; a != nil && p.now().Sub(a.at) <= waveSigFresh {
		return a.rssi
	}
	return fromRead
}

func (p *Poller) hear(d Device, a bluez.Advert) {
	r, err := switchbot.ParseThermometer(a.Data)
	if err != nil {
		p.log.Warn("advertisement not understood", "device", d.Name, "err", err)
		return
	}
	adv := &advert{at: p.now(), reading: r, rssi: a.RSSI}
	if b, ok := switchbot.Battery(a.Service); ok {
		adv.battery = &b
	}
	p.mu.Lock()
	p.heard[d.Name] = adv
	p.mu.Unlock()
}

// sampleThermometers queues each thermometer's newest advertisement, if it
// is recent enough; a thermometer unheard for longer is a gap, not a flat
// line of stale values.
func (p *Poller) sampleThermometers() {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range p.cfg.Thermometers {
		a := p.heard[d.Name]
		if a == nil || now.Sub(a.at) > p.cfg.ThermoStale {
			p.log.Warn("thermometer not heard", "device", d.Name)
			continue
		}
		p.sink.Add(p.series(d.Name, now, a.reading.Metrics(a.battery, a.rssi))...)
	}
}

func (p *Poller) series(device string, t time.Time, ms []metric.Metric) []promwrite.Series {
	out := make([]promwrite.Series, 0, len(ms))
	for _, m := range ms {
		out = append(out, promwrite.Series{
			Labels:  map[string]string{"__name__": m.Name, "job": p.cfg.Job, "device": device},
			Samples: []promwrite.Sample{{T: t, V: m.Value}},
		})
	}
	return out
}

// flush sends what is queued; a failed target keeps its samples for the next
// flush.
func (p *Poller) flush(ctx context.Context) {
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, p.cfg.FlushTimeout)
	defer cancel()
	if err := p.sink.Flush(ctx); err != nil {
		p.log.Warn("push failed", "err", err)
	}
}
