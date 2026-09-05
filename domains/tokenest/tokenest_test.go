package tokenest

import (
	"math"
	"testing"
)

// TestFromChars_MatchesLegacy35 confirms the consolidated helper reproduces
// the pre-C4 `len(body)/3.5` semantics exactly, so callers that previously
// divided by 3.5 are byte-for-byte unchanged.
func TestFromChars_MatchesLegacy35(t *testing.T) {
	cases := []int{0, 1, 7, 35, 350, 3500, 12345}
	for _, n := range cases {
		want := int(float64(n) / 3.5)
		if got := FromChars(n); got != want {
			t.Errorf("FromChars(%d) = %d, want legacy /3.5 = %d", n, got, want)
		}
	}
}

// TestFromChars_NotLegacy4 guards against regressing to the old V2 builder
// constant (chars/4), which understated tokens by ~12.5% and disagreed with
// the rest of the codebase.
func TestFromChars_NotLegacy4(t *testing.T) {
	// 400 chars: /3.5 ≈ 114, /4 = 100. They differ, and we must be on the 3.5 side.
	n := 400
	if FromChars(n) == n/4 {
		t.Fatalf("FromChars(%d) matched the legacy /4 estimate; expected the /3.5 value", n)
	}
}

func TestFromChars_NonPositive(t *testing.T) {
	if FromChars(0) != 0 {
		t.Error("FromChars(0) != 0")
	}
	if FromChars(-10) != 0 {
		t.Error("FromChars(-10) != 0")
	}
}

func TestFromChars_LargeInputDoesNotOverflow(t *testing.T) {
	values := []int{math.MaxInt / 4, math.MaxInt / 2, math.MaxInt}
	previous := 0
	for _, value := range values {
		got := FromChars(value)
		if got < 0 {
			t.Fatalf("FromChars(%d) = %d, want non-negative", value, got)
		}
		if got < previous {
			t.Fatalf("FromChars is not monotonic: %d after %d", got, previous)
		}
		previous = got
	}
}

func TestFromChars_Monotonic(t *testing.T) {
	previous := 0
	for value := 1; value <= 10000; value++ {
		got := FromChars(value)
		if got < previous {
			t.Fatalf("FromChars(%d) = %d after %d", value, got, previous)
		}
		previous = got
	}
}

func TestFromString(t *testing.T) {
	if got := FromString(""); got != 0 {
		t.Errorf("FromString(\"\") = %d, want 0", got)
	}
	if got := FromString("hello world"); got != FromChars(len("hello world")) {
		t.Error("FromString != FromChars(len)")
	}
}
