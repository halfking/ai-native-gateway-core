package secret

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

var ErrNoKey = errors.New("credential encryption key not configured")

func FernetKeyFromSecret(secretKey, explicit string) ([]byte, error) {
	key := explicit
	if key == "" {
		if secretKey == "" {
			return nil, ErrNoKey
		}
		sum := sha256.Sum256([]byte(secretKey))
		key = base64.URLEncoding.EncodeToString(sum[:])
	}
	raw, err := base64.URLEncoding.DecodeString(key)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(key)
	}
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("invalid fernet key length %d", len(raw))
	}
	return raw, nil
}

func DecryptFernet(token []byte, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("invalid fernet key length %d", len(key))
	}
	trimmed := bytes.TrimSpace(token)
	decoded := make([]byte, base64.URLEncoding.DecodedLen(len(trimmed)))
	n, err := base64.URLEncoding.Decode(decoded, trimmed)
	if err != nil {
		decoded = make([]byte, base64.RawURLEncoding.DecodedLen(len(trimmed)))
		n, err = base64.RawURLEncoding.Decode(decoded, trimmed)
	}
	if err != nil {
		return "", err
	}
	decoded = decoded[:n]
	if len(decoded) < 1+8+16+32 || decoded[0] != 0x80 {
		return "", errors.New("invalid fernet token")
	}
	msg := decoded[:len(decoded)-32]
	sig := decoded[len(decoded)-32:]
	signingKey := key[:16]
	encryptionKey := key[16:]
	mac := hmac.New(sha256.New, signingKey)
	mac.Write(msg)
	if !hmac.Equal(mac.Sum(nil), sig) {
		return "", errors.New("invalid fernet signature")
	}
	iv := decoded[9:25]
	ciphertext := decoded[25 : len(decoded)-32]
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}
	if len(ciphertext)%block.BlockSize() != 0 {
		return "", errors.New("invalid fernet ciphertext length")
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	plain, err = pkcs7Unpad(plain, block.BlockSize())
	if err != nil {
		return "", err
	}
	_ = time.Unix(int64(binaryBigEndian(decoded[1:9])), 0)
	return string(plain), nil
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid pkcs7 data")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("invalid pkcs7 padding")
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, errors.New("invalid pkcs7 padding")
		}
	}
	return data[:len(data)-pad], nil
}

func EncryptFernet(plaintext []byte, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid fernet key length %d", len(key))
	}
	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	encryptionKey := key[16:]
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plaintext, block.BlockSize())
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	ts := make([]byte, 8)
	binaryBigEndianWrite(ts, uint64(time.Now().Unix()))

	msg := make([]byte, 0, 1+8+16+len(ciphertext))
	msg = append(msg, 0x80)
	msg = append(msg, ts...)
	msg = append(msg, iv...)
	msg = append(msg, ciphertext...)

	signingKey := key[:16]
	mac := hmac.New(sha256.New, signingKey)
	mac.Write(msg)
	sig := mac.Sum(nil)

	token := make([]byte, len(msg)+32)
	copy(token, msg)
	copy(token[len(msg):], sig)

	encoded := make([]byte, base64.URLEncoding.EncodedLen(len(token)))
	base64.URLEncoding.Encode(encoded, token)
	return encoded, nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	padded := make([]byte, len(data)+pad)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	return padded
}

func binaryBigEndianWrite(b []byte, n uint64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(n)
		n >>= 8
	}
}

func binaryBigEndian(b []byte) uint64 {
	var n uint64
	for _, c := range b {
		n = n<<8 | uint64(c)
	}
	return n
}
