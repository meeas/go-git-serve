package main

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestFirstUpdateDelay(t *testing.T) {
	every := 15 * time.Minute
	if got := firstUpdateDelay(every, false, rand.N[time.Duration]); got != every {
		t.Errorf("not randomized: got %s, want %s", got, every)
	}
	if got := firstUpdateDelay(0, true, rand.N[time.Duration]); got != 0 {
		t.Errorf("interval disabled: got %s", got)
	}

	// Randomized delays stay within [0, every) and actually spread out:
	// 30 "students" should land in most of 10 equal slices of the interval.
	slices := map[int]bool{}
	for range 30 {
		d := firstUpdateDelay(every, true, rand.N[time.Duration])
		if d < 0 || d >= every {
			t.Fatalf("delay %s outside [0, %s)", d, every)
		}
		slices[int(d*10/every)] = true
	}
	if len(slices) < 6 {
		t.Errorf("30 random delays fell into only %d of 10 slices of the interval", len(slices))
	}
}
