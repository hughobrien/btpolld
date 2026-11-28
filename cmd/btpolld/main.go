// Command btpolld reads Bluetooth LE sensors through BlueZ and pushes every
// reading with remote write to each push URL in its config file. It serves
// nothing.
//
//	btpolld --config /etc/btpolld.toml
//
// The file (see internal/config, and deploy/btpolld.toml.example) names the
// devices, grouped by how they are read, and the receivers to push to. A push
// URL may hold a password, so keep the file readable by btpolld alone.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"syscall"

	"github.com/hughobrien/btpolld/internal/bluez"
	"github.com/hughobrien/btpolld/internal/config"
	"github.com/hughobrien/btpolld/internal/poll"
	"github.com/hughobrien/btpolld/internal/promwrite"
)

var version = "dev"

// exitRestartBluez is the exit status for poll.ErrScanDead and
// poll.ErrBluezStuck; the systemd unit restarts bluetoothd on it.
const exitRestartBluez = 3

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "btpolld:", err)
		if errors.Is(err, poll.ErrScanDead) || errors.Is(err, poll.ErrBluezStuck) {
			os.Exit(exitRestartBluez)
		}
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string) error {
	def := "/etc/btpolld.toml"
	if v := getenv("BTPOLLD_CONFIG"); v != "" {
		def = v
	}
	fs := flag.NewFlagSet("btpolld", flag.ContinueOnError)
	path := fs.String("config", def, "path of the TOML config file (or set BTPOLLD_CONFIG)")
	showVer := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVer {
		fmt.Println(versionString())
		return nil
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	targets, err := targets(cfg.Push)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	bt, err := bluez.Dial()
	if err != nil {
		return err
	}
	defer bt.Close()

	p := poll.New(poll.Config{Job: cfg.Job, Waves: cfg.Waves, Thermometers: cfg.Thermometers,
		WaveInterval: cfg.WaveInterval, ThermoInterval: cfg.ThermoInterval},
		bt, bt, promwrite.NewPushers(targets, nil), log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("started", "version", versionString(), "polled", len(cfg.Waves), "broadcasting", len(cfg.Thermometers), "targets", len(targets))
	return p.Run(ctx)
}

func versionString() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	return buildVersion(bi)
}

func buildVersion(bi *debug.BuildInfo) string {
	var rev string
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" {
		if len(rev) > 7 {
			rev = rev[:7]
		}
		if dirty {
			rev += "-dirty"
		}
		return rev
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

var notLabel = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// targets names each push URL after its host, for logs: credentials and path
// stay out of them.
func targets(urls []string) (promwrite.Targets, error) {
	var ts promwrite.Targets
	seen := map[string]int{}
	for _, s := range urls {
		u, err := url.Parse(s)
		if err != nil {
			return nil, errors.New("push: not a URL")
		}
		name := "h" + notLabel.ReplaceAllString(u.Hostname(), "_")
		if seen[name]++; seen[name] > 1 {
			name = fmt.Sprintf("%s_%d", name, seen[name])
		}
		ts = append(ts, promwrite.Target{Name: name, URL: s})
	}
	return ts, nil
}
