package dbx

import "testing"

func TestValidateIdentifier(t *testing.T) {
	for _, ok := range []string{"a", "tenant_id", "_x", "a1_b2", "request_logs_hot"} {
		if err := ValidateIdentifier(ok); err != nil {
			t.Errorf("ValidateIdentifier(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "1abc", "Adbc", "has space", "quote\"x", "semi;colon",
		"user; DROP TABLE users; --", "tab\tx", "dash-x", "ümlaut", "app.current_tenant",
	} {
		if err := ValidateIdentifier(bad); err == nil {
			t.Errorf("ValidateIdentifier(%q) = nil, want error", bad)
		}
	}
}

func TestQuoteIdentifier(t *testing.T) {
	q, err := QuoteIdentifier("tenant_id")
	if err != nil {
		t.Fatalf("QuoteIdentifier: %v", err)
	}
	if q != `"tenant_id"` {
		t.Fatalf("QuoteIdentifier = %s, want %q", q, `"tenant_id"`)
	}
	if _, err := QuoteIdentifier(`x"; DROP TABLE t; --`); err == nil {
		t.Fatal("QuoteIdentifier accepted injection payload, want ErrInvalidIdentifier")
	}
}
