# btpolld: notes for agents

Pushes Bluetooth LE sensor readings with remote write, running on a Raspberry Pi Zero W near the sensors, to a VictoriaMetrics on another host. Grew out of aird (the Wave Plus exporter; its repo is deleted). Siblings: `../kasad`, `../emporiad`, `../comfortd`, `../breezyd`. Follow their conventions.

## Hard rules

- **This repo is public.** No device addresses (MACs), device or room names, or locations in commits. Test fixtures are invented. The real addresses live only on the host that runs it (`/etc/btpolld.toml`).
- Read-only: only read the Wave Plus's current-values characteristic. Do not write to its command characteristics (`b42e2d06`, `b42e2fc2`) or its `f000ffc0` service (TI firmware update, probably) without Hugh agreeing. A SwitchBot thermometer is only listened to.
- Dependencies: `prometheus/client_golang` (only through `promwrite`), `godbus/dbus` and `BurntSushi/toml`. Ask before adding more.
- No nix, no HTTP server, no `/metrics`: Hugh wants it a simple standalone poller.

## Layout

- `cmd/btpolld`: `--config` (default `/etc/btpolld.toml`, or `BTPOLLD_CONFIG`) and `--version`; everything else is in the file.
- `internal/config`: the TOML file, validated; devices are grouped by how they are read (`[poll]`, `[broadcast]`), a `type` fits one group, and errors never echo values (push URLs hold passwords). `deploy/btpolld.toml.example` is tested to load.
- `internal/poll`: the poll loop (Wave Plus, up to three attempts) and the thermometer listeners (a changed advertisement is recorded at once, a steady one repeated each interval); pushes through a `Sink`.
- `internal/wave`, `internal/switchbot`: decode a device's data into `metric.Metric`s (names are the contract with the dashboard).
- `internal/bluez`: BlueZ over D-Bus: `Read` (find the adapter, scan if the device is unknown, connect, wait for `ServicesResolved`, `ReadValue`, disconnect) and `Listen` (passive scan on its own bus connection). The pure parts are tested; the D-Bus calls are only exercised on a real host. Beware shadowing the `service` constant (`org.bluez`): a call to the wrong destination makes dbus-broker drop the connection with a bare `EOF`.
- `internal/promwrite`: remote write 1.0 by hand, identical in all the daemons (SPDX header aside); change the copies together.
- `grafana/btpolld.json`: importable dashboard; `internal/poll/dashboard_test.go` fails if it uses a metric btpolld does not push.
- `deploy/btpolld.service`, `deploy/btpolld.toml.example`: systemd unit and example config for a Debian-style host.

## Things learned

- BlueZ forgets devices it has not seen: `Connect` on an unknown path fails, so scan first (done in `bluez.discover`).
- A Wave Plus is weak at a distance; connections fail sometimes, hence the retries. A new reading only appears every 2.5 to 5 minutes. It reports radon as 0 for its first days; values above 16383 mean "none".
- The default BlueZ D-Bus policy lets any user talk to `org.bluez`, so the service runs as an unprivileged user with no extra group.
- A SwitchBot thermometer needs no connection: temperature and humidity are in every advertisement; the battery (service data) only arrives at a good signal.
- The collector Pi is a Pi Zero W (armv6l: `GOARCH=arm GOARM=6`, `just build-pi`), Raspberry Pi OS. Its Bluetooth is soft-blocked by default (`rfkill unblock bluetooth`). It hears the sensors about 20 dB better than the server did, which is why it exists.

## Commands

`just test` (`go test -race ./...`).
