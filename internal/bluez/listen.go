package bluez

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	propsChanged     = "org.freedesktop.DBus.Properties.PropertiesChanged"
	dbusPath         = "/org/freedesktop/DBus"
	dbusIf           = "org.freedesktop.DBus"
	nameOwnerChanged = dbusIf + ".NameOwnerChanged"
	setupTimeout     = 30 * time.Second
)

// Advert is what BlueZ last learnt from a device's advertisements.
type Advert struct {
	Data []byte // manufacturer data of the wanted company, after its identifier
	// Service is the service data under the wanted UUID, nil until BlueZ has
	// seen it (a device may send it only in some advertisements).
	Service []byte
	RSSI    int16 // dBm; zero until BlueZ reports one
}

// apply merges changed Device1 properties into a, for the manufacturer data
// of company. It reports whether a holds data afterwards and anything
// changed, that is, whether an advertisement was just heard.
func (a *Advert) apply(company uint16, serviceUUID string, changed map[string]dbus.Variant) bool {
	touched := false
	if v, ok := changed["RSSI"]; ok {
		if r, ok := v.Value().(int16); ok {
			a.RSSI, touched = r, true
		}
	}
	if v, ok := changed["ManufacturerData"]; ok {
		if m, ok := v.Value().(map[uint16]dbus.Variant); ok {
			if d, ok := m[company]; ok {
				if b, ok := d.Value().([]byte); ok {
					a.Data, touched = append([]byte(nil), b...), true
				}
			}
		}
	}
	if v, ok := changed["ServiceData"]; ok {
		if m, ok := v.Value().(map[string]dbus.Variant); ok {
			for u, d := range m {
				if b, ok := d.Value().([]byte); ok && strings.EqualFold(u, serviceUUID) {
					a.Service, touched = append([]byte(nil), b...), true
				}
			}
		}
	}
	return touched && a.Data != nil
}

// seed loads what BlueZ already knows of the device at dev from a listing of
// its objects.
func (a *Advert) seed(objs objects, dev dbus.ObjectPath, company uint16, serviceUUID string) {
	a.apply(company, serviceUUID, objs[dev][deviceIf])
}

// Listen scans passively, never connecting, and calls fn each time the device
// at mac advertises (BlueZ reports a changed RSSI or manufacturer data), with
// the manufacturer data of company and the service data under serviceUUID.
// It returns when ctx ends or the scan cannot run. It uses its own bus connection, so its scan does not collide
// with the one Read starts.
func (c *Client) Listen(ctx context.Context, mac string, company uint16, serviceUUID string, fn func(Advert)) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("system bus: %w", err)
	}
	defer conn.Close()

	sctx, cancel := context.WithTimeout(ctx, setupTimeout)
	objs, err := (&Client{conn: conn}).managed(sctx)
	if err != nil {
		cancel()
		return fmt.Errorf("list bluez objects: %w", err)
	}
	adapter, ok := findAdapter(objs)
	if !ok {
		cancel()
		return errors.New("no bluetooth adapter (is bluetooth enabled?)")
	}
	dev := devicePath(adapter, mac)

	sigs := make(chan *dbus.Signal, 64)
	conn.Signal(sigs)
	defer conn.RemoveSignal(sigs)
	if err := conn.AddMatchSignalContext(sctx, dbus.WithMatchObjectPath(dev), dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged")); err != nil {
		cancel()
		return fmt.Errorf("watch device: %w", err)
	}
	if err := conn.AddMatchSignalContext(sctx, dbus.WithMatchObjectPath(dbusPath), dbus.WithMatchInterface(dbusIf), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, service)); err != nil {
		cancel()
		return fmt.Errorf("watch bluez: %w", err)
	}

	a := conn.Object(service, adapter)
	setDiscoveryFilter(sctx, a, mac, true)
	if err := a.CallWithContext(sctx, adapterIf+".StartDiscovery", 0).Err; err != nil {
		cancel()
		return fmt.Errorf("start scan: %w", err)
	}
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), disconnectT)
		defer dcancel()
		_ = a.CallWithContext(dctx, adapterIf+".StopDiscovery", 0).Err
	}()

	// BlueZ sends manufacturer data only when it changes, so a steady reading
	// (a thermometer in a fridge) would never be heard again after a restart:
	// start from what it already holds for the device, then refresh after
	// subscribing so changes between GetManagedObjects and AddMatchSignal are
	// not missed.
	var cur Advert
	cur.seed(objs, dev, company, serviceUUID)
	var props map[string]dbus.Variant
	if err := conn.Object(service, dev).CallWithContext(sctx, "org.freedesktop.DBus.Properties.GetAll", 0, deviceIf).Store(&props); err == nil {
		cur.apply(company, serviceUUID, props)
	}
	cancel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case s, ok := <-sigs:
			if !ok {
				return errors.New("bus connection closed")
			}
			if isBluezOwnerChanged(s) {
				return errors.New("bluetoothd restarted")
			}
			if s.Path != dev || s.Name != propsChanged || len(s.Body) < 2 {
				continue
			}
			if iface, _ := s.Body[0].(string); iface != deviceIf {
				continue
			}
			changed, _ := s.Body[1].(map[string]dbus.Variant)
			if cur.apply(company, serviceUUID, changed) {
				fn(cur)
			}
		}
	}
}

func isBluezOwnerChanged(s *dbus.Signal) bool {
	if s.Name != nameOwnerChanged || len(s.Body) < 3 {
		return false
	}
	name, _ := s.Body[0].(string)
	oldOwner, _ := s.Body[1].(string)
	newOwner, _ := s.Body[2].(string)
	return name == service && oldOwner != newOwner
}
