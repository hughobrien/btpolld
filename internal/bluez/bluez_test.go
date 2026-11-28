package bluez

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestDevicePath(t *testing.T) {
	got := devicePath("/org/bluez/hci0", "aa:bb:cc:00:11:22")
	if got != "/org/bluez/hci0/dev_AA_BB_CC_00_11_22" {
		t.Fatal(got)
	}
}

func TestFind(t *testing.T) {
	v := func(s string) map[string]dbus.Variant { return map[string]dbus.Variant{"UUID": dbus.MakeVariant(s)} }
	objs := objects{
		"/org/bluez/hci1":                          {adapterIf: {}},
		"/org/bluez/hci0":                          {adapterIf: {}},
		"/org/bluez/hci0/dev_AA/service0001/char1": {charIf: v("0000AAAA-0000-1000-8000-00805f9b34fb")},
		"/org/bluez/hci0/dev_AA/service0001/char2": {charIf: v("b42e2a68-ade7-11e4-89d3-123b93f75cba")},
		"/org/bluez/hci0/dev_BB/service0001/char2": {charIf: v("b42e2a68-ade7-11e4-89d3-123b93f75cba")},
	}
	if a, ok := findAdapter(objs); !ok || a != "/org/bluez/hci0" {
		t.Fatalf("adapter %q", a)
	}
	if _, ok := findAdapter(objects{}); ok {
		t.Fatal("no adapter expected")
	}
	p, ok := findCharacteristic(objs, "/org/bluez/hci0/dev_AA", "B42E2A68-ADE7-11E4-89D3-123B93F75CBA")
	if !ok || p != "/org/bluez/hci0/dev_AA/service0001/char2" {
		t.Fatalf("char %q %v", p, ok)
	}
	if _, ok := findCharacteristic(objs, "/org/bluez/hci0/dev_CC", "b42e2a68-ade7-11e4-89d3-123b93f75cba"); ok {
		t.Fatal("wrong device matched")
	}
}

func TestWaitTrue(t *testing.T) {
	n := 0
	if err := waitTrue(context.Background(), func() (bool, error) { n++; return n == 3, nil }); err != nil || n != 3 {
		t.Fatal(err, n)
	}
	boom := errors.New("boom")
	if err := waitTrue(context.Background(), func() (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := waitTrue(ctx, func() (bool, error) { return false, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestAdvertApply(t *testing.T) {
	const svc = "0000fd3d-0000-1000-8000-00805f9b34fb"
	var a Advert
	mfr := dbus.MakeVariant(map[uint16]dbus.Variant{0x0969: dbus.MakeVariant([]byte{1, 2, 3})})
	if a.apply(0x0969, svc, map[string]dbus.Variant{"RSSI": dbus.MakeVariant(int16(-90))}) {
		t.Error("RSSI alone, with no data yet, is not an advert")
	}
	if !a.apply(0x0969, svc, map[string]dbus.Variant{"ManufacturerData": mfr}) || len(a.Data) != 3 || a.RSSI != -90 {
		t.Errorf("got %+v", a)
	}
	// A later RSSI-only change is an advert with the data kept.
	if !a.apply(0x0969, svc, map[string]dbus.Variant{"RSSI": dbus.MakeVariant(int16(-95))}) || a.RSSI != -95 || len(a.Data) != 3 {
		t.Errorf("got %+v", a)
	}
	// Another company's data is ignored.
	var b Advert
	other := dbus.MakeVariant(map[uint16]dbus.Variant{0x0334: dbus.MakeVariant([]byte{9})})
	if b.apply(0x0969, svc, map[string]dbus.Variant{"ManufacturerData": other}) {
		t.Error("other company's data accepted")
	}
}

func TestAdvertApplyServiceData(t *testing.T) {
	const svc = "0000fd3d-0000-1000-8000-00805f9b34fb"
	a := Advert{Data: []byte{1}}
	sd := dbus.MakeVariant(map[string]dbus.Variant{svc: dbus.MakeVariant([]byte{0x77, 0, 0x64}), "0000abcd-0000-1000-8000-00805f9b34fb": dbus.MakeVariant([]byte{9})})
	if !a.apply(0x0969, svc, map[string]dbus.Variant{"ServiceData": sd}) || len(a.Service) != 3 || a.Service[2] != 0x64 {
		t.Errorf("got %+v", a)
	}
}

// A steady reading never changes in BlueZ, so Listen must start from what
// BlueZ already holds, or only RSSI updates arrive and nothing is heard.
func TestAdvertSeed(t *testing.T) {
	const svc = "0000fd3d-0000-1000-8000-00805f9b34fb"
	dev := dbus.ObjectPath("/org/bluez/hci0/dev_00_11_22_33_44_66")
	objs := objects{dev: {deviceIf: {
		"RSSI":             dbus.MakeVariant(int16(-84)),
		"ManufacturerData": dbus.MakeVariant(map[uint16]dbus.Variant{0x0969: dbus.MakeVariant([]byte{1, 2, 3})}),
		"ServiceData":      dbus.MakeVariant(map[string]dbus.Variant{svc: dbus.MakeVariant([]byte{0x77, 0, 0x64})}),
	}}}
	var a Advert
	a.seed(objs, dev, 0x0969, svc)
	if len(a.Data) != 3 || len(a.Service) != 3 || a.RSSI != -84 {
		t.Fatalf("seed: %+v", a)
	}
	// Later, an RSSI-only update is now an advert.
	if !a.apply(0x0969, svc, map[string]dbus.Variant{"RSSI": dbus.MakeVariant(int16(-88))}) {
		t.Error("RSSI update after seeding should be heard")
	}
	var b Advert
	b.seed(objs, "/org/bluez/hci0/dev_other", 0x0969, svc)
	if b.Data != nil {
		t.Error("unknown device seeded data")
	}
}

func TestIsDeviceAdded(t *testing.T) {
	dev := dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_00_11_22")
	withDev := map[string]map[string]dbus.Variant{deviceIf: {}}
	withoutDev := map[string]map[string]dbus.Variant{"org.bluez.GattService1": {}}
	cases := []struct {
		name string
		sig  *dbus.Signal
		want bool
	}{
		{"matching device", &dbus.Signal{Name: objManager + ".InterfacesAdded", Body: []any{dev, withDev}}, true},
		{"wrong path", &dbus.Signal{Name: objManager + ".InterfacesAdded", Body: []any{dbus.ObjectPath("/org/bluez/hci0/dev_other"), withDev}}, false},
		{"no Device1", &dbus.Signal{Name: objManager + ".InterfacesAdded", Body: []any{dev, withoutDev}}, false},
		{"unrelated signal", &dbus.Signal{Name: propsChanged, Body: []any{dev, withDev}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDeviceAdded(tc.sig, dev); got != tc.want {
				t.Errorf("isDeviceAdded() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsBluezOwnerChanged(t *testing.T) {
	cases := []struct {
		name string
		sig  *dbus.Signal
		want bool
	}{
		{"owner lost", &dbus.Signal{Name: nameOwnerChanged, Body: []any{service, ":1.5", ""}}, true},
		{"owner changed", &dbus.Signal{Name: nameOwnerChanged, Body: []any{service, ":1.5", ":1.9"}}, true},
		{"same owner", &dbus.Signal{Name: nameOwnerChanged, Body: []any{service, ":1.5", ":1.5"}}, false},
		{"other name", &dbus.Signal{Name: nameOwnerChanged, Body: []any{"org.example.Other", ":1.5", ""}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBluezOwnerChanged(tc.sig); got != tc.want {
				t.Errorf("isBluezOwnerChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}
