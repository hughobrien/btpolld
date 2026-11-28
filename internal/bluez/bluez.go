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
	c, err := dbus.ConnectSystemBus()
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
// exchange, scanning included.
func (c *Client) Read(ctx context.Context, mac, uuid string) (val []byte, err error) {
	objs, err := c.managed(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("list bluez objects: %w: %w", ErrUnresponsive, err)
	}
	if err != nil {
		return nil, fmt.Errorf("list bluez objects: %w", err)
	}
	adapter, ok := findAdapter(objs)
	if !ok {
		return nil, errors.New("no bluetooth adapter (is bluetooth enabled?)")
	}
	dev := devicePath(adapter, mac)
	if _, known := objs[dev]; !known {
		if err := c.discover(ctx, adapter, dev, mac); err != nil {
			return nil, err
		}
	}

	d := c.conn.Object(service, dev)
	// Disconnect even when Connect fails: a Connect that timed out here is
	// still pending in BlueZ, and connecting pauses the scan.
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), disconnectT)
		defer cancel()
		_ = d.CallWithContext(dctx, deviceIf+".Disconnect", 0).Err
	}()
	if err := d.CallWithContext(ctx, deviceIf+".Connect", 0).Err; err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	if err := waitTrue(ctx, func() (bool, error) {
		v, err := getProp(ctx, d, deviceIf, "ServicesResolved")
		if err != nil {
			return false, err
		}
		b, _ := v.Value().(bool)
		return b, nil
	}); err != nil {
		return nil, fmt.Errorf("resolve services: %w", err)
	}

	objs, err = c.managed(ctx)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	ch, ok := findCharacteristic(objs, dev, uuid)
	if !ok {
		return nil, fmt.Errorf("device has no characteristic %s", uuid)
	}
	err = c.conn.Object(service, ch).CallWithContext(ctx, charIf+".ReadValue", 0, map[string]dbus.Variant{}).Store(&val)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", uuid, err)
	}
	return val, nil
}

func getProp(ctx context.Context, o dbus.BusObject, iface, prop string) (dbus.Variant, error) {
	var v dbus.Variant
	err := o.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, prop).Store(&v)
	return v, err
}

// discover scans until BlueZ knows the device at path dev.
func (c *Client) discover(ctx context.Context, adapter, dev dbus.ObjectPath, mac string) error {
	sigs := make(chan *dbus.Signal, 64)
	c.conn.Signal(sigs)
	defer c.conn.RemoveSignal(sigs)

	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath("/"),
		dbus.WithMatchInterface(objManager),
		dbus.WithMatchMember("InterfacesAdded"),
	}
	if err := c.conn.AddMatchSignalContext(ctx, match...); err != nil {
		return fmt.Errorf("watch devices: %w", err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.Background(), disconnectT)
		defer cancel()
		_ = c.conn.RemoveMatchSignalContext(rctx, match...)
	}()

	a := c.conn.Object(service, adapter)
	setDiscoveryFilter(ctx, a, mac, false)
	if err := a.CallWithContext(ctx, adapterIf+".StartDiscovery", 0).Err; err != nil {
		return fmt.Errorf("start scan: %w", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), disconnectT)
		defer cancel()
		_ = a.CallWithContext(sctx, adapterIf+".StopDiscovery", 0).Err
	}()

	// Check once after subscribing in case the device appeared in the window
	// between GetManagedObjects and AddMatchSignal.
	if _, err := getProp(ctx, c.conn.Object(service, dev), deviceIf, "Address"); err == nil {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("device not seen while scanning: %w", ctx.Err())
		case s, ok := <-sigs:
			if !ok {
				return errors.New("device not seen while scanning: bus connection closed")
			}
			if isDeviceAdded(s, dev) {
				return nil
			}
		}
	}
}

func setDiscoveryFilter(ctx context.Context, a dbus.BusObject, mac string, duplicateData bool) {
	filter := map[string]dbus.Variant{
		"Transport": dbus.MakeVariant("le"),
		"Pattern":   dbus.MakeVariant(strings.ToUpper(mac)),
	}
	if duplicateData {
		filter["DuplicateData"] = dbus.MakeVariant(true)
	}
	if a.CallWithContext(ctx, adapterIf+".SetDiscoveryFilter", 0, filter).Err != nil {
		delete(filter, "Pattern")
		_ = a.CallWithContext(ctx, adapterIf+".SetDiscoveryFilter", 0, filter).Err
	}
}

func isDeviceAdded(s *dbus.Signal, dev dbus.ObjectPath) bool {
	if s.Name != objManager+".InterfacesAdded" || len(s.Body) < 2 {
		return false
	}
	path, ok := s.Body[0].(dbus.ObjectPath)
	if !ok || path != dev {
		return false
	}
	ifaces, ok := s.Body[1].(map[string]map[string]dbus.Variant)
	if !ok {
		return false
	}
	_, hasDev := ifaces[deviceIf]
	return hasDev
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
