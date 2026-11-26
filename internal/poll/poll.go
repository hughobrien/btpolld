// Package poll gets readings from the Bluetooth LE devices and pushes each
// one, at the time it was taken, to a Sink.
//
// An Airthings Wave Plus is polled: every interval the poller connects, reads
// its current values and disconnects. A SwitchBot thermometer is listened to:
// it broadcasts its values, and every advertisement heard becomes samples at
// the time it was heard. The poller keeps no memory of what it sent (no
// change detection, no repeating, no staleness limit): it forwards what it
// hears and leaves the judging to the server.
package poll

import (
	"context"
	"errors"
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
	Name     string // the device label; defaults to the address
	MAC      string
	Interval time.Duration     // zero takes the Config default for its kind
	Prefix   string            // replaces the family's metric name prefix (airthings, switchbot); empty keeps it
	Labels   map[string]string // extra labels on every series of this device
}

// Config holds the poller's settings. Zero durations and Attempts take the
// default in brackets.
type Config struct {
	Job          string // job label on pushed series
	Waves        []Device
	Thermometers []Device

	WaveInterval   time.Duration // default for how often to read a Wave Plus (15m)
	ThermoInterval time.Duration // default for how often to push what a thermometer's advertisements have become (1m)
	Attempts       int           // reads tried per Wave Plus per poll (3)
	Timeout        time.Duration // bound on one read, scanning and connecting included (45s)
	RetryDelay     time.Duration // pause between attempts, and before listening again (5s)
	FlushTimeout   time.Duration // bound on one write to a target (10s)
	DeadAfter      time.Duration // with a broadcast device, the scan is dead when nothing listened to is heard for this long (3m)
}

// ErrScanDead is what Run returns when nothing has been heard for
// Config.DeadAfter. Seen on a Pi Zero W: the controller stops scanning while
// bluetoothd still believes it is scanning, so it never starts a new scan and
// no client of it can recover; restarting bluetoothd does (the unit does that
// when btpolld exits with this error).
var ErrScanDead = errors.New("scan dead: nothing heard")

// ErrBluezStuck is what Run returns when bluetoothd has not answered
// (bluez.ErrUnresponsive) stuckAfter Wave Plus reads in a row. Seen on a Pi
// Zero W while advertisements still arrived; restarting bluetoothd cures it,
// as for ErrScanDead.
var ErrBluezStuck = errors.New("bluetoothd stuck: not answering")

// stuckAfter is how many reads in a row bluetoothd must leave unanswered
// before the run ends with ErrBluezStuck.
const stuckAfter = 2

// Poller polls and listens.
type Poller struct {
	cfg   Config
	rd    Reader
	ln    Listener
	sink  Sink
	log   *slog.Logger
	now   func() time.Time
	sleep func(context.Context, time.Duration)

	readMu  sync.Mutex // one read at a time: the adapter connects to one device
	flushMu sync.Mutex // one flush at a time
	mu      sync.Mutex
	alive   time.Time // when anything listened to was last heard, or Run started

	stop  context.CancelCauseFunc // ends Run with a cause
	stuck int                     // reads in a row bluetoothd did not answer; under readMu
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
	def(&cfg.Timeout, 45*time.Second)
	def(&cfg.RetryDelay, 5*time.Second)
	def(&cfg.FlushTimeout, 10*time.Second)
	def(&cfg.DeadAfter, 3*time.Minute)
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
	return &Poller{cfg: cfg, rd: rd, ln: ln, sink: sink, log: log, now: time.Now, sleep: sleepCtx, stop: func(error) {}}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Run polls and listens until ctx ends, the scan is dead (ErrScanDead) or
// bluetoothd is stuck (ErrBluezStuck), then flushes what is queued once more.
func (p *Poller) Run(ctx context.Context) error {
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	p.stop = stop
	p.noteHeard()
	var wg sync.WaitGroup
	if len(p.cfg.Thermometers) > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); p.watch(ctx, stop) }()
	}
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
	if err := context.Cause(ctx); errors.Is(err, ErrScanDead) || errors.Is(err, ErrBluezStuck) {
		return err
	}
	return nil
}

// watch ends the run with ErrScanDead once nothing listened to has been heard
// for DeadAfter. A thermometer is the pulsar: it advertises every 2 s and a
// healthy scan hears one at least every 22 s or so, so it only runs when a
// broadcast device is configured (a Wave Plus advert alone is not relied on).
func (p *Poller) watch(ctx context.Context, stop context.CancelCauseFunc) {
	t := time.NewTicker(p.cfg.DeadAfter / 6)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		p.mu.Lock()
		quiet := p.now().Sub(p.alive)
		p.mu.Unlock()
		if quiet > p.cfg.DeadAfter {
			p.log.Error("scan dead: nothing heard", "for", quiet.Round(time.Second))
			stop(ErrScanDead)
			return
		}
	}
}

// noteHeard notes that something listened to was just heard.
func (p *Poller) noteHeard() {
	p.mu.Lock()
	p.alive = p.now()
	p.mu.Unlock()
}

func (p *Poller) runWaves(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for _, d := range p.cfg.Waves {
		wg.Add(2)
		go func() { defer wg.Done(); p.listenWave(ctx, d) }()
		go func() {
			defer wg.Done()
			t := time.NewTicker(p.every(d, p.cfg.WaveInterval))
			defer t.Stop()
			for {
				p.pollWave(ctx, d)
				p.flush(ctx)
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
}

// every is a device's own interval, else the default for its kind.
func (p *Poller) every(d Device, def time.Duration) time.Duration {
	if d.Interval > 0 {
		return d.Interval
	}
	return def
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
	p.readMu.Lock()
	defer p.readMu.Unlock()
	var (
		r   wave.Reading
		err error
	)
	for attempt := 1; attempt <= p.cfg.Attempts; attempt++ {
		r, err = p.readWave(ctx, d)
		if err == nil {
			break
		}
		p.log.Warn("read failed", "device", d.Name, "attempt", attempt, "err", err)
		if p.noteStuck(err) {
			return
		}
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
	p.stuck = 0
	p.add(d, wave.Prefix, p.now(), r.Metrics())
	p.log.Info("read", "device", d.Name)
}

// noteStuck counts reads in a row that bluetoothd left unanswered and, at
// stuckAfter, ends the run with ErrBluezStuck and reports true. Any other
// failure (the device away, a failed connection) starts the count again.
// Call it with readMu held.
func (p *Poller) noteStuck(err error) bool {
	if !errors.Is(err, bluez.ErrUnresponsive) {
		p.stuck = 0
		return false
	}
	p.stuck++
	if p.stuck < stuckAfter {
		return false
	}
	p.log.Error("bluetoothd stuck: not answering", "reads", p.stuck)
	p.stop(ErrBluezStuck)
	return true
}

func (p *Poller) readWave(ctx context.Context, d Device) (wave.Reading, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	raw, _, err := p.rd.Read(ctx, d.MAC, wave.CurrentValues)
	if err != nil {
		return wave.Reading{}, err
	}
	r, err := wave.Parse(raw)
	if err != nil {
		return wave.Reading{}, fmt.Errorf("decode: %w", err)
	}
	return r, nil
}

func (p *Poller) runThermometers(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for _, d := range p.cfg.Thermometers {
		wg.Add(2)
		go func() { defer wg.Done(); p.listen(ctx, d) }()
		go func() {
			defer wg.Done()
			t := time.NewTicker(p.every(d, p.cfg.ThermoInterval))
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					p.flush(ctx)
				}
			}
		}()
	}
}

// listen keeps a scan for d going, restarting it after a failure.
func (p *Poller) listen(ctx context.Context, d Device) {
	for ctx.Err() == nil {
		err := p.ln.Listen(ctx, d.MAC, switchbot.CompanyID, switchbot.ServiceUUID, func(a bluez.Advert) { p.noteHeard(); p.hear(d, a) })
		if ctx.Err() != nil {
			return
		}
		p.log.Warn("listen failed", "device", d.Name, "err", err)
		p.sleep(ctx, p.cfg.RetryDelay)
	}
}

// listenWave keeps a scan for a Wave Plus going, only to learn its signal
// strength, which every advertisement heard reports; the values come from
// reading it.
func (p *Poller) listenWave(ctx context.Context, d Device) {
	for ctx.Err() == nil {
		err := p.ln.Listen(ctx, d.MAC, wave.CompanyID, "", func(a bluez.Advert) {
			p.noteHeard()
			p.add(d, wave.Prefix, p.now(), wave.SignalMetrics(a.RSSI))
		})
		if ctx.Err() != nil {
			return
		}
		p.log.Warn("listen failed", "device", d.Name, "err", err)
		p.sleep(ctx, p.cfg.RetryDelay)
	}
}

// hear turns an advertisement into samples stamped with the time it was heard.
func (p *Poller) hear(d Device, a bluez.Advert) {
	r, err := switchbot.ParseThermometer(a.Data)
	if err != nil {
		p.log.Warn("advertisement not understood", "device", d.Name, "err", err)
		return
	}
	var battery *uint8
	if b, ok := switchbot.Battery(a.Service); ok {
		battery = &b
	}
	p.add(d, switchbot.Prefix, p.now(), r.Metrics(battery, a.RSSI))
}

// add queues ms for d at time t, under the family's prefix (or d's own).
func (p *Poller) add(d Device, family string, t time.Time, ms []metric.Metric) {
	p.sink.Add(p.series(d, t, metric.Rename(ms, family, d.Prefix))...)
}

func (p *Poller) series(d Device, t time.Time, ms []metric.Metric) []promwrite.Series {
	out := make([]promwrite.Series, 0, len(ms))
	for _, m := range ms {
		labels := map[string]string{"__name__": m.Name, "job": p.cfg.Job, "device": d.Name}
		for k, v := range d.Labels {
			labels[k] = v
		}
		out = append(out, promwrite.Series{
			Labels:  labels,
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
