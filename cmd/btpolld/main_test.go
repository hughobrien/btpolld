package main

import (
	"os"
	"strings"
	"testing"
)

func TestTargetsNamedByHost(t *testing.T) {
	ts, err := targets([]string{"http://user:hunter2@192.0.2.1:8428", "https://metrics.example/api", "http://metrics.example:9"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"h192_0_2_1", "hmetrics_example", "hmetrics_example_2"}
	for i, w := range want {
		if ts[i].Name != w {
			t.Errorf("target %d named %q, want %q", i, ts[i].Name, w)
		}
	}
	if ts[0].URL != "http://user:hunter2@192.0.2.1:8428" {
		t.Error("the URL must be kept whole")
	}
}

func TestRunErrorsHideSecrets(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/c.toml"
	if err := writeFile(path, `push = ["ftp://user:hunter2@h"]`+"\n"); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"--config", path}, func(string) string { return "" })
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("%v", err)
	}
}

func TestMissingConfigNamesThePath(t *testing.T) {
	err := run([]string{"--config", "/nonexistent/c.toml"}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/c.toml") {
		t.Fatalf("%v", err)
	}
}

func writeFile(path, s string) error { return os.WriteFile(path, []byte(s), 0o600) }

// The README shows the example config and the unit; they must be the files in
// deploy/, not copies that have drifted.
func TestReadmeShowsDeployFiles(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"btpolld.toml.example", "btpolld.service"} {
		b, err := os.ReadFile("../../deploy/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(readme), strings.TrimRight(string(b), "\n")) {
			t.Errorf("README.md does not contain deploy/%s verbatim", f)
		}
	}
}
