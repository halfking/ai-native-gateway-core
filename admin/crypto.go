package admin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"math/big"
	mrand "math/rand"
	"time"
)

func randomAlphanum(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, n)
	for i := range result {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		result[i] = charset[idx.Int64()]
	}
	return string(result)
}

func init() {
	mrand.Seed(time.Now().UnixNano())
}

func encryptFernet(plaintext []byte, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errInvalidKeyLen
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
	padded := pkcs7PadAdmin(plaintext, block.BlockSize())
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	ts := make([]byte, 8)
	binaryBigEndianWriteAdmin(ts, uint64(time.Now().Unix()))

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

func maskAPIKey(plaintext string) string {
	if len(plaintext) <= 16 {
		return "****"
	}
	return plaintext[:10] + "..." + plaintext[len(plaintext)-6:]
}

func decryptFernet(token []byte, key []byte) (string, error) {
	if len(key) != 32 {
		return "", errInvalidKeyLen
	}
	decoded := make([]byte, base64.URLEncoding.DecodedLen(len(token)))
	n, err := base64.URLEncoding.Decode(decoded, token)
	if err != nil {
		decoded = make([]byte, base64.RawURLEncoding.DecodedLen(len(token)))
		n, err = base64.RawURLEncoding.Decode(decoded, token)
	}
	if err != nil {
		return "", err
	}
	decoded = decoded[:n]
	if len(decoded) < 1+8+16+32 || decoded[0] != 0x80 {
		return "", errInvalidToken
	}
	msg := decoded[:len(decoded)-32]
	sig := decoded[len(decoded)-32:]
	signingKey := key[:16]
	encryptionKey := key[16:]
	mac := hmac.New(sha256.New, signingKey)
	mac.Write(msg)
	if !hmac.Equal(mac.Sum(nil), sig) {
		return "", errInvalidSig
	}
	iv := decoded[9:25]
	ct := decoded[25 : len(decoded)-32]
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}
	if len(ct)%block.BlockSize() != 0 {
		return "", errInvalidCiphertext
	}
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)
	plain, err = pkcs7UnpadAdmin(plain, block.BlockSize())
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func pkcs7PadAdmin(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	padded := make([]byte, len(data)+pad)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	return padded
}

func pkcs7UnpadAdmin(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errInvalidPadding
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, errInvalidPadding
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, errInvalidPadding
		}
	}
	return data[:len(data)-pad], nil
}

func binaryBigEndianWriteAdmin(b []byte, n uint64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(n)
		n >>= 8
	}
}

var (
	errInvalidKeyLen    = errorf("invalid fernet key length")
	errInvalidToken     = errorf("invalid fernet token")
	errInvalidSig       = errorf("invalid fernet signature")
	errInvalidCiphertext = errorf("invalid fernet ciphertext length")
	errInvalidPadding   = errorf("invalid pkcs7 padding")
)

type strErr string

func (e strErr) Error() string { return string(e) }
func errorf(s string) error    { return strErr(s) }
