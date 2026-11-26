# btpolld: notes for agents

Pushes Bluetooth LE sensor readings with remote write, running on a Raspberry Pi Zero W near the sensors, pushing to a VictoriaMetrics on another host. Grew out of aird (the Wave Plus exporter; its repo is deleted). Siblings: `../kasad`, `../emporiad`, `../comfortd`, `../breezyd`. Follow their conventions.

## Hard rules

- **This repo is public.** No device addresses (MACs), device or room names, or locations in commits. Test fixtures are invented. The real addresses live only on the host that runs it (`/etc/btpolld.toml`).
- Read-only: only read the Wave Plus's current-values characteristic. Do not write to its command characteristics (`b42e2d06`, `b42e2fc2`) or its `f000ffc0` service (TI firmware update, probably) without Hugh agreeing. A SwitchBot thermometer is only listened to.
- Dependencies: `prometheus/client_golang` (only through `promwrite`), `godbus/dbus` and `BurntSushi/toml`. Ask before adding more.
- No HTTP server, no `/metrics`, no nix packaging or module: Hugh wants it a simple standalone poller. `flake.nix` exists only so the forge's CI runner (nix and nothing else) can build and test it: it has one output, `checks.<system>.gate`.

## Layout

- `cmd/btpolld`: `--config` (default `/etc/btpolld.toml`, or `BTPOLLD_CONFIG`) and `--version`; everything else is in the file.
- `internal/config`: the TOML file, validated; devices are grouped by how they are read (`[poll]`, `[broadcast]`), a `type` fits one group, and errors never echo values (push URLs hold passwords). `[family.<type>] prefix` renames the `airthings`/`switchbot` part of the metric names for every device of that type (via `metric.Rename`); the default leaves the names alone, and the dashboard assumes the defaults. A device's `labels` table adds labels to its series (not `job`, `device` or `__*`). `deploy/btpolld.toml.example` is tested to load.
- `internal/poll`: the poll loop (Wave Plus, up to three attempts) and the thermometer listeners (every advertisement heard becomes samples at once; the Wave Plus listener forwards only its signal strength); pushes through a `Sink`.
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
- The scan can die silently: the controller stops scanning while bluetoothd still reports `Discovering=true` (btmon shows no advertising reports; Stop Discovery is answered "Rejected"), so bluetoothd never starts a new scan for any client and restarting btpolld does not help; restarting bluetoothd does. Hence `poll.ErrScanDead`, exit status 3 and the unit's `ExecStopPost`. bluetoothd can also stop answering while adverts still arrive: `GetManagedObjects` takes the whole 45 s read timeout (busctl gets it in about 2 s), so every Wave Plus read fails; that is `bluez.ErrUnresponsive`, and two in a row end the run with `poll.ErrBluezStuck` (also exit 3). Only that error counts, never a device being away, and the unit restarts bluetoothd at most every 30 minutes, so a dead device cannot cause a restart storm. Healthy, measured with btmon: about 200 reports/s from about 120 addresses; a SwitchBot thermometer advertises every 2.0 s and about 40% are heard at -82 dBm (longest gap 22 s); a Wave Plus every 0.5 s (longest gap 4 s).
- `btmon -w` still prints every frame to stdout: under `systemd-run` give it `StandardOutput=null`, or it floods the Pi's small journal (and drops frames, faking a dead scan).

## The collector keeps no state

Hugh's rule: the Pi is puny, the server is a 12-core i7, so btpolld forwards what it hears and the server judges it. Every advertisement of a `[broadcast]` device becomes samples stamped with when it was heard (about 0.2 a second for a thermometer), `interval` is only the push cadence, and the Wave Plus listener pushes `airthings_rssi_dbm` from each of its adverts. No change detection, no repeating a steady value, no staleness limit, no carrying a last value forward: do not add any. Decoders refuse nothing but data too short to parse (or a Wave Plus version byte they do not know): out-of-range humidity, a signal strength of 0 and the like go to the server as they came, and cleanup is the server's job (Hugh, 2026-11-26).

BlueZ sends a property only when it changes, so `bluez.Listen` first seeds itself from the device's properties in `GetManagedObjects`; without it a steady reading (a thermometer in a fridge, a Wave Plus's fixed serial) is never heard after a restart. BlueZ also keeps the last service data, so the battery arrives with every advert once heard.

## CI

`.forgejo/workflows/test.yml` runs `nix build .#checks.x86_64-linux.gate` on the forge's runner: `go vet`, a gofmt check, `go test` and `go test -race` in nix's sandbox (as in the sibling daemons). The gate must pass before a merge to `main`, which is protected (no direct pushes). When `go.mod` or `go.sum` change, update `vendorHash` in `flake.nix`: set it to `pkgs.lib.fakeHash`, run the build on bigthink, paste the `got:` hash. Run it with `nix build .#checks.x86_64-linux.gate -L` on a nix host.

## Commands

`just test` (`go test -race ./...`).
