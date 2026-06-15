package admin

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"unicode"
)

const defaultTenantAdminUserCode = "user"

// DefaultTenantAdminUsername returns the default tenant_admin login name: {tenant_code}user.
func DefaultTenantAdminUsername(tenantCode string) string {
	return tenantCode + defaultTenantAdminUserCode
}

// GenerateTenantAdminPassword returns a one-time initial password for a new tenant admin.
// Format satisfies ValidatePasswordComplexity (upper, lower, digit, special).
func GenerateTenantAdminPassword(tenantCode string) string {
	return fmt.Sprintf("Tenant%s@2026", tenantCode)
}

// generateRandomPassword returns a 12-char URL-safe random password with mixed case and digits.
func generateRandomPassword() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// base64 URL encoding yields upper, lower, digits; append "A1!" for policy compliance.
	return base64.RawURLEncoding.EncodeToString(b)[:12] + "A1!", nil
}

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
