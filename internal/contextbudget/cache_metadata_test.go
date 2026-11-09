package contextbudget

import (
	"strings"
	"testing"
	"time"
)

type opaqueMetadata struct{}

func (opaqueMetadata) MarshalJSON() ([]byte, error) {
	panic("sizing must not invoke custom marshalers")
}

func TestMetadataSizingIsBoundedBeforeSerialization(t *testing.T) {
	if !MetadataFits(map[string]any{"hash": strings.Repeat("f", 64), "at": time.Now()}, 1024) {
		t.Fatal("small normal metadata was rejected")
	}
	for _, value := range []any{strings.Repeat("x", 1<<20), opaqueMetadata{}, []int{1, 2, 3, 4}} {
		if MetadataFits(value, 128) {
			t.Fatal("oversized or opaque metadata was accepted")
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if MetadataFits(cycle, 1<<20) {
		t.Fatal("cyclic metadata was accepted")
	}
}
