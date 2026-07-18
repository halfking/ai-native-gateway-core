package sanitize

import (
	"testing"
)

func TestPlaceholder_String(t *testing.T) {
	tests := []struct {
		name  string
		input Placeholder
		want  string
	}{
		{"phone", Placeholder{TypePhone, 0}, "{SENSITIVE:phone:0}"},
		{"id_card", Placeholder{TypeIDCard, 1}, "{SENSITIVE:id_card:1}"},
		{"email", Placeholder{TypeEmail, 2}, "{SENSITIVE:email:2}"},
		{"credit_card", Placeholder{TypeCreditCard, 0}, "{SENSITIVE:credit_card:0}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.String(); got != tt.want {
				t.Errorf("Placeholder.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParsePlaceholder(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantTyp SensitiveType
		wantIdx int
	}{
		{"valid phone", "{SENSITIVE:phone:0}", true, TypePhone, 0},
		{"valid id_card", "{SENSITIVE:id_card:3}", true, TypeIDCard, 3},
		{"valid email", "{SENSITIVE:email:1}", true, TypeEmail, 1},
		{"valid custom", "{SENSITIVE:custom:42}", true, TypeCustom, 42},
		{"no braces", "SENSITIVE:phone:0", false, "", 0},
		{"wrong format", "{SENSITIVE:phone}", false, "", 0},
		{"empty", "", false, "", 0},
		{"multiple colons", "{SENSITIVE:phone:0:extra}", false, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParsePlaceholder(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ParsePlaceholder() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok {
				if got.Type != tt.wantTyp {
					t.Errorf("ParsePlaceholder() type = %v, want %v", got.Type, tt.wantTyp)
				}
				if got.Index != tt.wantIdx {
					t.Errorf("ParsePlaceholder() index = %v, want %v", got.Index, tt.wantIdx)
				}
			}
		})
	}
}

func TestValidateSensitiveType(t *testing.T) {
	tests := []struct {
		input SensitiveType
		want  bool
	}{
		{TypePhone, true},
		{TypeIDCard, true},
		{TypeEmail, true},
		{TypeCreditCard, true},
		{TypeSecret, true},
		{TypeInternalIP, true},
		{TypeName, true},
		{TypeCustom, true},
		{SensitiveType("unknown"), false},
		{SensitiveType(""), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			if got := ValidateSensitiveType(tt.input); got != tt.want {
				t.Errorf("ValidateSensitiveType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlaceholderPattern(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"{SENSITIVE:phone:0}", 1},
		{"{SENSITIVE:id_card:42}", 1},
		{"plain text with no placeholders", 0},
		{"one {SENSITIVE:phone:0} and two {SENSITIVE:email:1}", 2},
		{"multiple {SENSITIVE:phone:0} same {SENSITIVE:phone:0}", 2},
	}
	for _, tt := range tests {
		t.Run(tt.input[:min(len(tt.input), 30)], func(t *testing.T) {
			matches := PlaceholderPattern.FindAllString(tt.input, -1)
			if len(matches) != tt.want {
				t.Errorf("PlaceholderPattern matches = %d, want %d", len(matches), tt.want)
			}
		})
	}
}
