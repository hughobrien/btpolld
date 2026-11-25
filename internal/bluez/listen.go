package bluez

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

const propsChanged = "org.freedesktop.DBus.Properties.PropertiesChanged"

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

	objs, err := (&Client{conn: conn}).managed(ctx)
	if err != nil {
		return fmt.Errorf("list bluez objects: %w", err)
	}
	adapter, ok := findAdapter(objs)
	if !ok {
		return errors.New("no bluetooth adapter (is bluetooth enabled?)")
	}
	dev := devicePath(adapter, mac)

	sigs := make(chan *dbus.Signal, 64)
	conn.Signal(sigs)
	defer conn.RemoveSignal(sigs)
	if err := conn.AddMatchSignalContext(ctx, dbus.WithMatchObjectPath(dev), dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged")); err != nil {
		return fmt.Errorf("watch device: %w", err)
	}

	a := conn.Object(service, adapter)
	filter := map[string]dbus.Variant{"Transport": dbus.MakeVariant("le"), "DuplicateData": dbus.MakeVariant(true)}
	_ = a.CallWithContext(ctx, adapterIf+".SetDiscoveryFilter", 0, filter).Err
	if err := a.CallWithContext(ctx, adapterIf+".StartDiscovery", 0).Err; err != nil {
		return fmt.Errorf("start scan: %w", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), disconnectT)
		defer cancel()
		_ = a.CallWithContext(sctx, adapterIf+".StopDiscovery", 0).Err
	}()

	var cur Advert
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case s, ok := <-sigs:
			if !ok {
				return errors.New("bus connection closed")
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
