# btpolld

Reads Bluetooth LE sensors through BlueZ and pushes every reading with remote
write (VictoriaMetrics, or Prometheus with its receiver on). It serves
nothing: no `/metrics`, no HTTP. Go; the dependencies are `godbus/dbus`,
`BurntSushi/toml` and, through the shared `promwrite` package, `prometheus/client_golang`. It
grew out of aird, which only knew the Wave Plus (and is gone).

## Devices

Each kind of device is known to the code: how to read it and what it
reports. You name the ones you have in the config file, in the section for how
they are read.

| `type` | Device | Section | How |
|---|---|---|---|
| `wave-plus` | Airthings Wave Plus | `[poll]` | connect, read the current values, disconnect, every 15 minutes |
| `switchbot-thermometer` | SwitchBot thermometer | `[broadcast]` | listen for its advertisements (no connection); record each changed reading as heard, and repeat a steady one every minute |

A device has to be polled when its advertisements do not carry its values (the
Wave Plus advertises only its serial number); one that does is listened to,
which costs it nothing.

```
airthings_co2_ppm airthings_voc_ppb airthings_temperature_celsius
airthings_humidity_percent airthings_pressure_hpa airthings_light
airthings_radon_short_term_bq_m3 airthings_radon_long_term_bq_m3   (absent until the device has one)
airthings_rssi_dbm                (signal strength at the host, from its advertisements)

switchbot_temperature_celsius switchbot_humidity_percent
switchbot_dew_point_celsius switchbot_absolute_humidity_g_m3 switchbot_vapor_pressure_deficit_kpa   (computed)
switchbot_battery_percent    (absent until the device sends it)
switchbot_rssi_dbm           (signal strength at the host)
```

Every series carries `job` (`job` in the config, default `btpolld`) and `device`. A device
that cannot be read or heard simply has no sample: a gap, not a flat line.

## Running

```
btpolld --config /etc/btpolld.toml
```

`push` is a list of remote write URLs; credentials may go in the URL
(`http://user:password@host:8428`), so keep the file readable by btpolld alone.
`interval` is set per section, and a device can override it. The host needs
BlueZ running. Errors never repeat what the file says.

### Config

`/etc/btpolld.toml` (also in `deploy/btpolld.toml.example`; the addresses are invented):

```toml
# btpolld config. Install as /etc/btpolld.toml, owned by root and readable by
# the btpolld user only (chmod 0640, chgrp btpolld): a push URL may hold a
# password. The addresses below are invented.

# Where to push, as remote write base URLs (VictoriaMetrics, or Prometheus with
# its receiver on). One is the usual case; with several, each gets everything.
# Credentials may go in the URL: http://user:password@host:8428
push = ["http://metrics.example:8428"]

# The `job` label on every pushed series. Optional.
job = "btpolld"

# Devices we connect to: read the current values, disconnect. A connection
# costs the device's battery and fails now and then (it is retried), so poll
# no faster than the device updates.
[poll]
interval = "15m"                # default for every device in this section

  [[poll.device]]
  name = "den"                  # becomes the `device` label
  type = "wave-plus"            # Airthings Wave Plus
  mac  = "AA:BB:CC:00:11:01"

  [[poll.device]]
  name = "loft"
  type = "wave-plus"
  mac  = "AA:BB:CC:00:11:02"
  interval = "10m"              # overrides the section default

# Devices that broadcast their values: we only listen, never connect.
# A reading is recorded the moment a changed one is heard; `interval` is how
# often a steady reading is repeated (so it plots as a line, not a gap) and how
# often what has been recorded is pushed. A device unheard for three intervals
# is left as a gap.
[broadcast]
interval = "1m"

  [[broadcast.device]]
  name = "probe"
  type = "switchbot-thermometer"
  mac  = "AA:BB:CC:00:11:03"
```

### systemd

`deploy/btpolld.service`:

```ini
# systemd unit for btpolld on a plain Linux host with BlueZ (Raspberry Pi OS,
# Debian). Copy deploy/btpolld.toml.example to /etc/btpolld.toml, fill it in,
# and make it readable by btpolld alone (it may hold a push password):
#
#   useradd --system btpolld
#   install -m 0755 btpolld /usr/local/bin/
#   install -m 0640 -g btpolld btpolld.toml.example /etc/btpolld.toml
#
[Unit]
Description=Bluetooth LE sensors to remote write
Wants=bluetooth.service
After=bluetooth.service network-online.target
Wants=network-online.target

[Service]
User=btpolld
ExecStart=/usr/local/bin/btpolld --config /etc/btpolld.toml
Restart=on-failure
RestartSec=30s
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallFilter=@system-service
SystemCallFilter=~@privileged

[Install]
WantedBy=multi-user.target
```

`just build-pi` makes a static binary for a Raspberry Pi Zero (armv6).
`grafana/btpolld.json` is an importable dashboard (Wave Plus only so far).

## Example: a Pi as a remote collector

Bluetooth LE reaches a few metres. The host that stores the metrics is often
not within that range of the sensors, or is not on the same network at all
(a server in the cloud). So run btpolld on something small that is near them
(a Raspberry Pi Zero W is enough) and let it push over the network:

```
sensors ~~BLE~~> Pi running btpolld ---remote write---> VictoriaMetrics
                                                        (home server, or in the cloud)
```

The Pi keeps nothing but the config file and a queue of the last few hours of
samples, which it resends if the receiver is unreachable. In one setup the server's own adapter heard a Wave Plus at -84 dBm and a
SwitchBot thermometer at -96 dBm (too weak for the thermometer's battery
reports); the Pi heard them at -64 and -74 dBm, about 20 dB better.

- On a trusted LAN a receiver without authentication is workable: firewall it
  to the LAN and let the Pi push to its address.
- Across the internet put TLS and authentication in front of the receiver
  (a reverse proxy with basic auth), and give the Pi the credentials in the URL:
  `push = ["https://user:password@metrics.example.com"]`. Keep
  `/etc/btpolld.toml` readable by btpolld alone.
- Build for the Pi with `just build-pi` (armv6, static, so one file to copy),
  install the unit from `deploy/`, and `rfkill unblock bluetooth` if Raspberry
  Pi OS has it blocked.

## Protocols

**Wave Plus**: service `b42e1c08-ade7-11e4-89d3-123b93f75cba`, characteristic
`b42e2a68-ade7-11e4-89d3-123b93f75cba`, 20 bytes: version, humidity in half
percent, light, one unused byte, then little-endian uint16: radon short-term,
radon long-term, temperature in hundredths of a degree, pressure in fiftieths
of a hPa, CO2, VOC and two unused. Radon above 16383 means none yet. The device
only updates every few minutes and its history can't be read (see
`PRIOR_ART.md`).

**SwitchBot thermometer**: manufacturer data (company 0x0969) is the six
address bytes, two status bytes, then temperature tenths (low four bits),
whole degrees (low seven bits; the top bit is set when not below zero) and
humidity (low seven bits). Service data under `fd3d` is a type byte, a status
byte and the battery percentage (low seven bits). The scan runs on its own
D-Bus connection so it does not disturb a Wave Plus read. Dew point, absolute
humidity and vapour-pressure deficit are computed (Magnus) and match the
vendor's app. Only an indoor/outdoor thermo-hygrometer has been checked.
