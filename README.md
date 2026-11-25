# btpolld

Reads Bluetooth LE sensors through BlueZ and pushes every reading with remote
write (VictoriaMetrics, or Prometheus with its receiver on). It serves
nothing: no `/metrics`, no HTTP. Go; the dependencies are `godbus/dbus` and,
through the shared `promwrite` package, `prometheus/client_golang`. It
descends from aird, which only knew the Wave Plus and is in git history.

## Devices

Each kind of device is known to the code: how to read it and what it
reports. You name the ones you have.

| Flag | Device | How |
|---|---|---|
| `--wave [name=]address` | Airthings Wave Plus | connect, read the current values, disconnect, every 15 minutes |
| `--thermometer [name=]address` | SwitchBot thermometer | listen for its advertisements (no connection), record the newest every minute |

Both repeat. The name is the `device` label (the address if none).

```
airthings_co2_ppm airthings_voc_ppb airthings_temperature_celsius
airthings_humidity_percent airthings_pressure_hpa airthings_light
airthings_radon_short_term_bq_m3 airthings_radon_long_term_bq_m3   (absent until the device has one)

switchbot_temperature_celsius switchbot_humidity_percent
switchbot_dew_point_celsius switchbot_absolute_humidity_g_m3 switchbot_vapor_pressure_deficit_kpa   (computed)
switchbot_battery_percent    (absent until the device sends it)
switchbot_rssi_dbm           (signal strength at the host)
```

Every series carries `job` (`--job`, default `btpolld`) and `device`. A device
that cannot be read or heard simply has no sample: a gap, not a flat line.

## Running

```
btpolld --wave den=00:11:22:33:44:55 --thermometer probe=00:11:22:33:44:66 \
        --push local=http://metrics.example:8428
```

`--push name=url` repeats; credentials may go in the URL
(`http://user:password@host:8428`). Every flag can instead be set in the
environment as `BTPOLLD_<FLAG>` (`BTPOLLD_WAVE`, `BTPOLLD_PUSH`,
`BTPOLLD_THERMOMETER_INTERVAL`, ...), several values separated by spaces, so
the addresses and passwords can live in a root-only file. `deploy/btpolld.service`
is a systemd unit that does that. The host needs BlueZ running.

`just build-pi` makes a static binary for a Raspberry Pi Zero (armv6).
`grafana/btpolld.json` is an importable dashboard (Wave Plus only so far).

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
