//go:build !integration

package collector

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstanceIDFromToken(t *testing.T) {
	token := buildUnsignedToken(t, "instance:inst-abc")
	id, err := InstanceIDFromToken(token)
	require.NoError(t, err)
	assert.Equal(t, "inst-abc", id)
}

func buildUnsignedToken(t *testing.T, subject string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{Subject: subject})
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)
	return signed
}
