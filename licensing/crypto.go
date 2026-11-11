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
	"strings"
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
	if c == nil || c.PublicKey == nil {
		return nil, errors.New("crypto config or public key not initialized (LICENSE_DISABLED=true)")
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
	block, _ := pem.Decode([]byte(NormalizeInlinePEM(pemStr)))
	if block == nil {
		return nil, errors.New("invalid PEM")
	}
	
	// Try PKIX format first (BEGIN PUBLIC KEY)
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err == nil {
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("not RSA public key")
		}
		return rsaPub, nil
	}
	
	// Fall back to PKCS1 format (BEGIN RSA PUBLIC KEY)
	rsaPub, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
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

// NormalizeInlinePEM 把「单行环境变量形态」的 PEM 还原成标准多行形态。
//
// ── 背景（2026-11-11 48h 审计 · 154 实测）────────────────────────────
// systemd 的 EnvironmentFile **不接受含真实换行的赋值**——154 的
// /etc/llm-gateway-go/env 里 LLM_GATEWAY_LICENSE_PUBLIC_KEY 以原生多行 PEM
// 写入 ⇒ systemd 逐行解析时把首行之后的每行都当独立赋值，整段
// 「Ignoring invalid environment assignment」丢弃 ⇒ 变量从未进入进程 ⇒
// 网关启动只有一行 Warn（"license verification disabled"），公钥配置意图
// 被静默吞掉。本函数让 env 文件可以写**单行**形态，两种等价写法都收：
//
//	① 转义形态：换行写成字面 `\n`（`-----BEGIN...-----\nMIIB...`）；
//	② Base64 整块：整段 PEM 做 StdEncoding（不含 -----BEGIN 前缀 ⇒ 可判）。
//
// 标准多行输入原样返回（不改既有行为）。还原失败（既非 PEM 开头、
// base64 也解不出 PEM）⇒ 返回原串，让 pem.Decode 报原本的错误——
// 本函数只负责「形态还原」，不做校验裁决。
func NormalizeInlinePEM(pemStr string) string {
	const pemHeader = "-----BEGIN"
	trimmed := strings.TrimSpace(pemStr)
	if trimmed == "" {
		return pemStr
	}
	if strings.Contains(trimmed, pemHeader) && strings.Contains(trimmed, "\n") {
		return pemStr // 标准多行形态：原样返回
	}
	if strings.Contains(trimmed, pemHeader) {
		// 单行但保留字面 `\n` 转义 ⇒ 还原换行。
		return strings.ReplaceAll(trimmed, `\n`, "\n")
	}
	// 不含 PEM 头 ⇒ 视为整块 Base64；解不出（或解出后仍无 PEM 头）⇒ 原样退回。
	if dec, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
		if s := string(dec); strings.Contains(s, pemHeader) {
			return s
		}
	}
	return pemStr
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
