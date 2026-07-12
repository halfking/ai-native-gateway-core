package activation

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// EncryptOfflineRequest AES-GCM 加密离线激活请求
// 密钥 = SHA256(license_key)
func EncryptOfflineRequest(licenseKey string, plaintext []byte) (string, error) {
	// 生成 AES 密钥
	keyHash := sha256.Sum256([]byte(licenseKey))

	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	// 生成 12 字节随机 nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// 加密
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	// Base64 编码
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptOfflineLicense AES-GCM 解密离线 license
func DecryptOfflineLicense(licenseKey string, ciphertextB64 string) ([]byte, error) {
	// Base64 解码
	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	// 生成 AES 密钥
	keyHash := sha256.Sum256([]byte(licenseKey))

	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, encrypted := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}

// OfflineRequestPayload 离线激活请求载荷
type OfflineRequestPayload struct {
	LicenseKey   string `json:"license_key"`
	HardwareHash string `json:"hardware_hash"`
	InstanceID   string `json:"instance_id"`
	DeviceName   string `json:"device_name"`
	Timestamp    string `json:"timestamp"`
}

// MarshalOfflineRequest 序列化并加密离线请求
func MarshalOfflineRequest(payload *OfflineRequestPayload) (string, error) {
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	return EncryptOfflineRequest(payload.LicenseKey, plaintext)
}

// UnmarshalOfflineRequest 解密并反序列化离线请求
func UnmarshalOfflineRequest(licenseKey, encrypted string) (*OfflineRequestPayload, error) {
	plaintext, err := DecryptOfflineLicense(licenseKey, encrypted)
	if err != nil {
		return nil, err
	}

	var payload OfflineRequestPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	return &payload, nil
}
