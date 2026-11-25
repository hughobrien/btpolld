// Command btpolld reads Bluetooth LE sensors through BlueZ and pushes every
// reading with remote write to each --push target. It serves nothing.
//
// Devices are named on the command line, one flag per kind of device:
//
//	--wave name=00:11:22:33:44:55         Airthings Wave Plus, connected to and read
//	--thermometer name=00:11:22:33:44:66  SwitchBot thermometer, listened for
//
// Every flag may also be set in the environment (BTPOLLD_WAVE, BTPOLLD_PUSH
// and so on, several values separated by spaces), so that a push URL with a
// password in it can live in a root-only file instead of the process list.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hughobrien/btpolld/internal/bluez"
	"github.com/hughobrien/btpolld/internal/poll"
	"github.com/hughobrien/btpolld/internal/promwrite"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "btpolld:", err)
		os.Exit(1)
	}
}

// devices is a repeatable flag: [name=]address.
type devices []poll.Device

func (ds *devices) String() string {
	var s []string
	for _, d := range *ds {
		s = append(s, d.Name+"="+d.MAC)
	}
	return strings.Join(s, ",")
}

func (ds *devices) Set(v string) error {
	d, err := parseDevice(v)
	if err != nil {
		return err
	}
	for _, o := range *ds {
		if o.Name == d.Name {
			return errors.New("a name is given twice")
		}
	}
	*ds = append(*ds, d)
	return nil
}

// parseDevice reads "name=00:11:22:33:44:55" or a bare address. Errors leave
// the address out: it names a device in someone's home.
func parseDevice(v string) (poll.Device, error) {
	name, addr, ok := strings.Cut(v, "=")
	if !ok {
		name, addr = "", v
	}
	hw, err := net.ParseMAC(addr)
	if err != nil || len(hw) != 6 {
		return poll.Device{}, errors.New("want [name=]00:11:22:33:44:55")
	}
	mac := strings.ToUpper(hw.String())
	if name == "" {
		name = mac
	}
	return poll.Device{Name: name, MAC: mac}, nil
}

// setFromEnv sets each flag still unset from BTPOLLD_<NAME>, splitting
// repeatable ones on white space. An error names the variable, never its
// value (it may hold a password).
func setFromEnv(fs *flag.FlagSet, getenv func(string) string) error {
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		env := "BTPOLLD_" + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		v := getenv(env)
		if v == "" || seen[f.Name] || err != nil {
			return
		}
		for _, one := range strings.Fields(v) {
			if e := f.Value.Set(one); e != nil {
				err = fmt.Errorf("%s: %v", env, strings.ReplaceAll(e.Error(), one, "(value)"))
				return
			}
		}
	})
	return err
}

func run(args []string, getenv func(string) string) error {
	var (
		targets promwrite.Targets
		waves   devices
		thermos devices
	)
	fs := flag.NewFlagSet("btpolld", flag.ContinueOnError)
	fs.Var(&targets, "push", "name=url of a remote write receiver (VictoriaMetrics, Prometheus with its receiver on) to push every reading to, as http://user:password@host:port; repeat for more")
	fs.Var(&waves, "wave", "[name=]address of an Airthings Wave Plus to read; repeat for more. The name is the device label (the address if none)")
	fs.Var(&thermos, "thermometer", "[name=]address of a SwitchBot thermometer to listen for; repeat for more")
	var (
		waveEvery   = fs.Duration("wave-interval", 15*time.Minute, "how often to read each Wave Plus")
		thermoEvery = fs.Duration("thermometer-interval", time.Minute, "how often to record each thermometer's newest advertisement")
		job         = fs.String("job", "btpolld", "job label on pushed series")
		showVer     = fs.Bool("version", false, "print version and exit")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVer {
		fmt.Println(version)
		return nil
	}
	if err := setFromEnv(fs, getenv); err != nil {
		return err
	}
	if len(waves) == 0 && len(thermos) == 0 {
		return errors.New("--wave or --thermometer is required")
	}
	if len(targets) == 0 {
		return errors.New("--push is required")
	}
	if *waveEvery <= 0 || *thermoEvery <= 0 {
		return errors.New("--wave-interval and --thermometer-interval must be positive")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	bt, err := bluez.Dial()
	if err != nil {
		return err
	}
	defer bt.Close()

	p := poll.New(poll.Config{Job: *job, Waves: waves, Thermometers: thermos, WaveInterval: *waveEvery, ThermoInterval: *thermoEvery},
		bt, bt, promwrite.NewPushers(targets, pushClient()), log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("started", "version", version, "waves", len(waves), "thermometers", len(thermos), "targets", len(targets))
	p.Run(ctx)
	return nil
}

// pushClient opens a fresh connection for each push. Pushes are a minute or
// more apart, about when VictoriaMetrics closes an idle connection, so a kept
// one is often found dead (EOF) just as it is reused.
func pushClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
}
