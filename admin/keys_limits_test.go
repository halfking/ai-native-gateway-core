package admin

import (
	"encoding/json"
	"testing"
)

func TestLimitFieldUnmarshalJSON(t *testing.T) {
	t.Run("null", func(t *testing.T) {
		var f limitField
		if err := json.Unmarshal([]byte(`null`), &f); err != nil {
			t.Fatal(err)
		}
		if !f.set || !f.isNull {
			t.Fatalf("expected set null field, got set=%v isNull=%v", f.set, f.isNull)
		}
		arg, err := f.sqlArg(100, "rate_limit_rpm")
		if err != nil || arg != nil {
			t.Fatalf("sqlArg(null) = %v, %v", arg, err)
		}
	})

	t.Run("zero unlimited", func(t *testing.T) {
		var f limitField
		if err := json.Unmarshal([]byte(`0`), &f); err != nil {
			t.Fatal(err)
		}
		arg, err := f.sqlArg(100, "rate_limit_rpm")
		if err != nil || arg != 0 {
			t.Fatalf("sqlArg(0) = %v, %v", arg, err)
		}
	})

	t.Run("custom", func(t *testing.T) {
		var f limitField
		if err := json.Unmarshal([]byte(`42`), &f); err != nil {
			t.Fatal(err)
		}
		arg, err := f.sqlArg(100, "rate_limit_rpm")
		if err != nil || arg != 42 {
			t.Fatalf("sqlArg(42) = %v, %v", arg, err)
		}
	})

	t.Run("missing field", func(t *testing.T) {
		var f limitField
		if _, err := f.sqlArg(100, "rate_limit_rpm"); err == nil {
			t.Fatal("expected error for unset field")
		}
	})

	t.Run("out of range", func(t *testing.T) {
		var f limitField
		if err := json.Unmarshal([]byte(`10001`), &f); err != nil {
			t.Fatal(err)
		}
		if _, err := f.sqlArg(10000, "rate_limit_rpm"); err == nil {
			t.Fatal("expected bound error")
		}
	})
}

func TestLimitFieldUpdateBodyRequiresAllFields(t *testing.T) {
	var body struct {
		RateLimitRPM        limitField `json:"rate_limit_rpm"`
		RateLimitConcurrent limitField `json:"rate_limit_concurrent"`
		RateLimitTPM        limitField `json:"rate_limit_tpm"`
	}
	if err := json.Unmarshal([]byte(`{"rate_limit_rpm":60}`), &body); err != nil {
		t.Fatal(err)
	}
	if _, err := body.RateLimitConcurrent.sqlArg(1000, "rate_limit_concurrent"); err == nil {
		t.Fatal("expected missing concurrent field error")
	}
}
