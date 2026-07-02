// Package secrets 提供 secrets 随机生成与 .env 文件管理
package secrets

import (
	"crypto/rand"
	"encoding/hex"
)

// GenerateRandom 生成 32 字节 hex 随机串（64 个字符）
func GenerateRandom() (string, error) {
	return GenerateRandomBytes(32)
}

// GenerateRandomBytes 生成 N 字节的 hex 随机串
func GenerateRandomBytes(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GeneratePassword 生成强密码（48 字符 base64）
func GeneratePassword() (string, error) {
	b := make([]byte, 36)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
