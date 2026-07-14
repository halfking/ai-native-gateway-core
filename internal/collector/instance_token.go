package collector

import (
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// InstanceIDFromToken extracts instance:<id> from a locally stored JWT subject.
func InstanceIDFromToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	parser := jwt.NewParser()
	var claims jwt.RegisteredClaims
	_, _, err := parser.ParseUnverified(token, &claims)
	if err != nil {
		return "", err
	}
	instanceID := strings.TrimPrefix(claims.Subject, "instance:")
	if instanceID == "" || instanceID == claims.Subject {
		return "", jwt.ErrTokenInvalidClaims
	}
	return instanceID, nil
}
