package metric

import "testing"

func TestRename(t *testing.T) {
	in := []Metric{{"airthings_co2_ppm", 1}, {"other_thing", 2}}
	out := Rename(in, "airthings", "wave")
	if out[0].Name != "wave_co2_ppm" || out[1].Name != "other_thing" || out[0].Value != 1 {
		t.Fatalf("%v", out)
	}
	if in[0].Name != "airthings_co2_ppm" {
		t.Error("the input was changed")
	}
	for _, to := range []string{"", "airthings"} {
		if got := Rename(in, "airthings", to); got[0].Name != "airthings_co2_ppm" {
			t.Errorf("to %q renamed: %v", to, got)
		}
	}
}
