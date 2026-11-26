// Package bluez reads a GATT characteristic from a BLE device through BlueZ,
// over the system D-Bus: find the device (scanning if BlueZ has not seen it),
// connect, read, disconnect.
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
	service     = "org.bluez"
	adapterIf   = "org.bluez.Adapter1"
	deviceIf    = "org.bluez.Device1"
	charIf      = "org.bluez.GattCharacteristic1"
	objManager  = "org.freedesktop.DBus.ObjectManager"
	pollEvery   = 250 * time.Millisecond
	disconnectT = 5 * time.Second
)

// objects is what ObjectManager.GetManagedObjects returns.
type objects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// Client talks to BlueZ.
type Client struct {
	conn *dbus.Conn
}

// Dial connects to the system bus.
func Dial() (*Client, error) {
	c, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	return &Client{conn: c}, nil
}

// Close releases the bus connection.
func (c *Client) Close() error { return c.conn.Close() }

// ErrUnresponsive marks a Read whose first call to bluetoothd, listing its
// objects, took the whole timeout. That call involves no device (busctl gets
// it back in about 2 s on a Pi Zero W), so it means bluetoothd is stuck, not
// that the device is away.
var ErrUnresponsive = errors.New("bluetoothd did not answer")

func (c *Client) managed(ctx context.Context) (objects, error) {
	var o objects
	err := c.conn.Object(service, "/").CallWithContext(ctx, objManager+".GetManagedObjects", 0).Store(&o)
	return o, err
}

// Read connects to the device at mac (as 00:11:22:33:44:55), reads the
// characteristic with the given UUID and disconnects. ctx bounds the whole
// exchange, scanning included. rssi is the signal strength BlueZ last heard
// from the device in dBm, or zero if it has none.
func (c *Client) Read(ctx context.Context, mac, uuid string) (val []byte, rssi int16, err error) {
	objs, err := c.managed(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, 0, fmt.Errorf("list bluez objects: %w: %w", ErrUnresponsive, err)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("list bluez objects: %w", err)
	}
	adapter, ok := findAdapter(objs)
	if !ok {
		return nil, 0, errors.New("no bluetooth adapter (is bluetooth enabled?)")
	}
	dev := devicePath(adapter, mac)
	if _, known := objs[dev]; !known {
		if err := c.discover(ctx, adapter, dev); err != nil {
			return nil, 0, err
		}
	}

	d := c.conn.Object(service, dev)
	if v, err := d.GetProperty(deviceIf + ".RSSI"); err == nil {
		rssi, _ = v.Value().(int16)
	}
	// Disconnect even when Connect fails: a Connect that timed out here is
	// still pending in BlueZ, and connecting pauses the scan.
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), disconnectT)
		defer cancel()
		_ = d.CallWithContext(dctx, deviceIf+".Disconnect", 0).Err
	}()
	if err := d.CallWithContext(ctx, deviceIf+".Connect", 0).Err; err != nil {
		return nil, 0, fmt.Errorf("connect: %w", err)
	}

	if err := waitTrue(ctx, func() (bool, error) {
		v, err := d.GetProperty(deviceIf + ".ServicesResolved")
		if err != nil {
			return false, err
		}
		b, _ := v.Value().(bool)
		return b, nil
	}); err != nil {
		return nil, 0, fmt.Errorf("resolve services: %w", err)
	}

	objs, err = c.managed(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list bluez objects: %w", err)
	}
	ch, ok := findCharacteristic(objs, dev, uuid)
	if !ok {
		return nil, 0, fmt.Errorf("device has no characteristic %s", uuid)
	}
	err = c.conn.Object(service, ch).CallWithContext(ctx, charIf+".ReadValue", 0, map[string]dbus.Variant{}).Store(&val)
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", uuid, err)
	}
	return val, rssi, nil
}

// discover scans until BlueZ knows the device at path dev.
func (c *Client) discover(ctx context.Context, adapter, dev dbus.ObjectPath) error {
	a := c.conn.Object(service, adapter)
	filter := map[string]dbus.Variant{"Transport": dbus.MakeVariant("le")}
	_ = a.CallWithContext(ctx, adapterIf+".SetDiscoveryFilter", 0, filter).Err
	if err := a.CallWithContext(ctx, adapterIf+".StartDiscovery", 0).Err; err != nil {
		return fmt.Errorf("start scan: %w", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), disconnectT)
		defer cancel()
		_ = a.CallWithContext(sctx, adapterIf+".StopDiscovery", 0).Err
	}()
	err := waitTrue(ctx, func() (bool, error) {
		o, err := c.managed(ctx)
		if err != nil {
			return false, err
		}
		_, ok := o[dev]
		return ok, nil
	})
	if err != nil {
		return fmt.Errorf("device not seen while scanning: %w", err)
	}
	return nil
}

// waitTrue calls f until it reports true, fails, or ctx ends.
func waitTrue(ctx context.Context, f func() (bool, error)) error {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		ok, err := f()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// findAdapter returns the first adapter (the lowest path, for determinism).
func findAdapter(objs objects) (dbus.ObjectPath, bool) {
	var best dbus.ObjectPath
	for p, ifs := range objs {
		if _, ok := ifs[adapterIf]; ok && (best == "" || p < best) {
			best = p
		}
	}
	return best, best != ""
}

// devicePath is where BlueZ puts the device with this address.
func devicePath(adapter dbus.ObjectPath, mac string) dbus.ObjectPath {
	return dbus.ObjectPath(string(adapter) + "/dev_" + strings.NewReplacer(":", "_", "-", "_").Replace(strings.ToUpper(mac)))
}

// findCharacteristic returns the path of the device's characteristic with the
// given UUID.
func findCharacteristic(objs objects, dev dbus.ObjectPath, uuid string) (dbus.ObjectPath, bool) {
	prefix := string(dev) + "/"
	for p, ifs := range objs {
		props, ok := ifs[charIf]
		if !ok || !strings.HasPrefix(string(p), prefix) {
			continue
		}
		if u, _ := props["UUID"].Value().(string); strings.EqualFold(u, uuid) {
			return p, true
		}
	}
	return "", false
}
