package licensing

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type CryptoConfig struct {
	PrivateKey    *rsa.PrivateKey
	PublicKey     *rsa.PublicKey
	AESKey        []byte
	JWTSecret     []byte
	DefaultExpiry time.Duration
}

func (c *CryptoConfig) SignLicense(lic *License) (*SignedLicense, error) {
	data, err := json.Marshal(lic)
	if err != nil {
		return nil, err
	}

	hash := sha256.Sum256(data)
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.PrivateKey, crypto.SHA256, hash[:])
	if err != nil {
		return nil, err
	}

	return &SignedLicense{
		Data:      data,
		Signature: signature,
	}, nil
}

func (c *CryptoConfig) VerifyLicense(signed *SignedLicense) (*License, error) {
	if signed == nil {
		return nil, errors.New("nil signed license")
	}

	hash := sha256.Sum256(signed.Data)
	if err := rsa.VerifyPKCS1v15(c.PublicKey, crypto.SHA256, hash[:], signed.Signature); err != nil {
		return nil, errors.New("license signature invalid")
	}

	var lic License
	if err := json.Unmarshal(signed.Data, &lic); err != nil {
		return nil, err
	}

	return &lic, nil
}

func (c *CryptoConfig) EncryptAES(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.AESKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, nil), nil
}

func (c *CryptoConfig) DecryptAES(encrypted []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.AESKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(encrypted) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ciphertext := encrypted[:nonceSize], encrypted[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func (c *CryptoConfig) GenerateJWT(instanceID string, licenseKey string, expiresAt time.Time) (string, error) {
	claims := jwt.MapClaims{
		"instance_id": instanceID,
		"license_key": licenseKey,
		"exp":         expiresAt.Unix(),
		"iat":         time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(c.JWTSecret)
}

func (c *CryptoConfig) VerifyJWT(tokenStr string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return c.JWTSecret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

func LoadPrivateKeyFromPEM(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("invalid PEM")
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		priv, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
	}
	rsaPriv, ok := priv.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not RSA private key")
	}
	return rsaPriv, nil
}

func LoadPublicKeyFromPEM(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("invalid PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("not RSA public key")
	}
	return rsaPub, nil
}

func MarshalToBase64(signed *SignedLicense) (string, error) {
	data, err := json.Marshal(signed)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func UnmarshalFromBase64(b64 string) (*SignedLicense, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	var signed SignedLicense
	if err := json.Unmarshal(data, &signed); err != nil {
		return nil, err
	}
	return &signed, nil
}
