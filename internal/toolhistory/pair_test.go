package toolhistory

import (
	"errors"
	"reflect"
	"testing"
)

func TestPairUsesLocalCallOrder(t *testing.T) {
	got, err := Pair([]string{"a", "b", "c"}, []string{"c", "a"})
	if err != nil || !reflect.DeepEqual(got, []int{1, -1, 0}) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, ids := range [][]string{{"x", "x"}, {""}} {
		if _, err := Pair(ids, nil); !errors.Is(err, ErrAmbiguousHistory) {
			t.Fatalf("ambiguous calls %q: %v", ids, err)
		}
	}
	if _, err := Pair([]string{"x"}, []string{"x", "x"}); !errors.Is(err, ErrAmbiguousHistory) {
		t.Fatalf("duplicate results: %v", err)
	}
}
