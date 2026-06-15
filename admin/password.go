package admin

import (
	"fmt"
	"unicode"
)

// ValidatePasswordComplexity enforces minimum password policy:
//   - Min 8 chars
//   - At least 1 uppercase letter
//   - At least 1 lowercase letter
//   - At least 1 digit
// Special characters are allowed but not required.
func ValidatePasswordComplexity(pw string) error {
	if len(pw) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	var hasUpper, hasLower, hasDigit bool
	for _, c := range pw {
		switch {
		case unicode.IsUpper(c):
			hasUpper = true
		case unicode.IsLower(c):
			hasLower = true
		case unicode.IsDigit(c):
			hasDigit = true
		}
	}
	if !hasUpper {
		return fmt.Errorf("password must contain at least 1 uppercase letter")
	}
	if !hasLower {
		return fmt.Errorf("password must contain at least 1 lowercase letter")
	}
	if !hasDigit {
		return fmt.Errorf("password must contain at least 1 digit")
	}
	return nil
}
