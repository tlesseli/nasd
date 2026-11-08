package health

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSensors(t *testing.T) {
	sys := t.TempDir()
	chip := filepath.Join(sys, "class", "hwmon", "hwmon0")
	os.MkdirAll(chip, 0o755)
	for f, v := range map[string]string{
		"name": "it87", "temp1_input": "42000", "temp1_label": "System", "temp1_crit": "70000",
		"fan1_input": "1250", "fan1_min": "500", "in0_input": "1200",
	} {
		os.WriteFile(filepath.Join(chip, f), []byte(v+"\n"), 0o644)
	}
	s := Sensors(sys)
	if len(s) != 2 {
		t.Fatalf("want 2 sensors (voltage ignored), got %+v", s)
	}
	fan, temp := s[0], s[1]
	if fan.Kind != "fan" || fan.Value != 1250 || fan.Limit != 500 || fan.Label != "fan1" {
		t.Errorf("fan: %+v", fan)
	}
	if temp.Kind != "temp" || temp.Value != 42 || temp.Limit != 70 || temp.Label != "System" || temp.Chip != "it87" {
		t.Errorf("temp: %+v", temp)
	}
}
