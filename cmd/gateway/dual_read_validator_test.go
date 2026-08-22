package main

import (
	"context"
	"reflect"
	"testing"
)

func TestDualReadValidator_Constructor(t *testing.T) {
	v := NewDualReadValidator(nil) // nil pool must not panic
	if v == nil {
		t.Fatal("nil validator")
	}
	// Verify the Compare method exists with the right signature via reflection.
	// NumIn on a bound method value excludes the receiver, so we expect
	// ctx + tenant + session + lastN = 4 inputs.
	rv := reflect.ValueOf(v)
	compare := rv.MethodByName("Compare")
	if !compare.IsValid() {
		t.Fatal("Compare method not set")
	}
	mt := compare.Type()
	if mt.NumIn() != 4 || mt.NumOut() != 2 {
		t.Fatalf("Compare signature mismatch: in=%d out=%d", mt.NumIn(), mt.NumOut())
	}
}

func TestDualReadValidator_NilPoolCompare(t *testing.T) {
	v := NewDualReadValidator(nil)
	_, err := v.Compare(context.Background(), "t1", "s1", 10)
	if err == nil {
		t.Fatal("expected error when pool is nil, got nil")
	}
	if err.Error() == "" {
		t.Fatalf("expected non-empty error message, got %v", err)
	}
}
