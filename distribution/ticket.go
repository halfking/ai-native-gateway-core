package distribution

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type TicketClaims struct {
	RequestID      string `json:"rid"`
	ReleaseVersion string `json:"ver"`
	Platform       string `json:"plt"`
	Arch           string `json:"arch"`
	ExpiresUnix    int64  `json:"exp"`
}

type TicketSigner struct {
	secret []byte
	ttl    time.Duration
}

func NewTicketSigner(secret []byte, ttl time.Duration) *TicketSigner {
	if len(secret) == 0 {
		secret = []byte("kx-gateway-download-dev-secret")
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &TicketSigner{secret: secret, ttl: ttl}
}

func (s *TicketSigner) Issue(requestID, version, platform, arch string) (token string, expiresAt time.Time, err error) {
	expiresAt = time.Now().Add(s.ttl)
	claims := TicketClaims{
		RequestID:      requestID,
		ReleaseVersion: version,
		Platform:       platform,
		Arch:           arch,
		ExpiresUnix:    expiresAt.Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig, expiresAt, nil
}

func (s *TicketSigner) Verify(token string) (*TicketClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid ticket format")
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return nil, fmt.Errorf("invalid ticket signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var claims TicketClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	if time.Now().Unix() > claims.ExpiresUnix {
		return nil, fmt.Errorf("ticket expired")
	}
	return &claims, nil
}
