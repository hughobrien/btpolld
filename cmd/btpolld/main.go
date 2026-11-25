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
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/hughobrien/btpolld/internal/bluez"
	"github.com/hughobrien/btpolld/internal/config"
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
		fmt.Println(version)
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
		bt, bt, promwrite.NewPushers(targets, pushClient()), log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("started", "version", version, "polled", len(cfg.Waves), "broadcasting", len(cfg.Thermometers), "targets", len(targets))
	p.Run(ctx)
	return nil
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

// pushClient opens a fresh connection for each push. Pushes are a minute or
// more apart, about when VictoriaMetrics closes an idle connection, so a kept
// one is often found dead (EOF) just as it is reused.
func pushClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
}
