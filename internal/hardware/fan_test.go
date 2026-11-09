package hardware

import (
	"testing"

	"readynas/internal/store"
)

func TestPercentFixed(t *testing.T) {
	c := store.FanSettings{Mode: "fixed", FixedPercent: 40}
	for hottest, want := range map[int]int{30: 40, FixedSafetyTemp - 1: 40, FixedSafetyTemp: 100, 0: 100} {
		if got := Percent(c, hottest); got != want {
			t.Errorf("fixed 40%% at %d °C: got %d, want %d", hottest, got, want)
		}
	}
}
