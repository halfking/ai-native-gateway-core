package licensing

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const activationCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
const activationCodeLength = 8

func GenerateActivationCode() (string, error) {
	code := make([]byte, activationCodeLength)
	max := big.NewInt(int64(len(activationCodeAlphabet)))
	for i := range code {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate activation code: %w", err)
		}
		code[i] = activationCodeAlphabet[n.Int64()]
	}
	return string(code), nil
}

func NormalizeActivationCode(code string) string {
	out := make([]byte, 0, len(code))
	for i := 0; i < len(code); i++ {
		c := code[i]
		if c == '-' || c == ' ' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}
