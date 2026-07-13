package licensing

import "testing"

func TestGenerateActivationCode(t *testing.T) {
	code, err := GenerateActivationCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != activationCodeLength {
		t.Fatalf("expected %d chars, got %q", activationCodeLength, code)
	}
}

func TestNormalizeActivationCode(t *testing.T) {
	if got := NormalizeActivationCode(" ab-cd "); got != "ABCD" {
		t.Fatalf("got %q", got)
	}
}
