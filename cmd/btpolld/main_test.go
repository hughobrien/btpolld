package main

import (
	"flag"
	"strings"
	"testing"
)

func TestParseDevice(t *testing.T) {
	d, err := parseDevice("den=aa:bb:cc:00:11:22")
	if err != nil || d.Name != "den" || d.MAC != "AA:BB:CC:00:11:22" {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = parseDevice("aa-bb-cc-00-11-22")
	if err != nil || d.Name != "AA:BB:CC:00:11:22" {
		t.Fatalf("%+v %v", d, err)
	}
	for _, bad := range []string{"", "den=", "den=nonsense", "aa:bb:cc"} {
		if _, err := parseDevice(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDevicesFlagRejectsDuplicateName(t *testing.T) {
	var ds devices
	if err := ds.Set("a=aa:bb:cc:00:11:22"); err != nil {
		t.Fatal(err)
	}
	if err := ds.Set("a=aa:bb:cc:00:11:23"); err == nil {
		t.Fatal("duplicate name accepted")
	}
}

func TestSetFromEnv(t *testing.T) {
	var ds devices
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.Var(&ds, "wave", "")
	job := fs.String("job", "btpolld", "")
	if err := fs.Parse([]string{"--job", "given"}); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BTPOLLD_WAVE": "a=aa:bb:cc:00:11:22 b=aa:bb:cc:00:11:23", "BTPOLLD_JOB": "fromenv"}
	if err := setFromEnv(fs, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || *job != "given" {
		t.Fatalf("%v %q", ds, *job)
	}
}

func TestSetFromEnvErrorHidesValue(t *testing.T) {
	var ds devices
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.Var(&ds, "wave", "")
	err := setFromEnv(fs, func(k string) string {
		if k == "BTPOLLD_WAVE" {
			return "den=hunter2"
		}
		return ""
	})
	if err == nil || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "BTPOLLD_WAVE") {
		t.Fatalf("%v", err)
	}
}
