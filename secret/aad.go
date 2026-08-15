// aad.go — domain-separated AES-GCM encryption with caller-supplied AAD.
//
// 背景：docs/修订0811/18-供应商耗尽长时保活与持久恢复设计-2026-08-14.md §11.2
// 要求新增 domain-separated EncryptWithAAD/DecryptWithAAD 安全 API，返回实际
// key_id；现有固定 `llm-gateway:credential` AAD 的 EncryptAESGCM 不可直接用于
// durable 数据。SR-16（durable 快照/结果加密）与 SC-6（sanitize Phase-2 明文
// 统计加密，docs/修订0811/19 §2）共用本原语：单 keyring、多 AAD domain。
//
// Envelope 格式：v2|<domain>|<kid>|<base64url(nonce12 || ciphertext || tag16)>
// （分隔符用 "|"：domain 本身含 ":"，kid 禁止包含 "|"。）
//
// AAD 由 domain + 绑定字段（tenant/task/request hash）以长度前缀方式规范拼接，
// 防止字段边界歧义导致的 AAD 混淆。
//
// fail-closed 三条件（doc 18 §11.2「无密钥、未知版本、AAD/hash 不匹配」）：
//   - 无密钥：keyring 为 nil 或 envelope 内 kid 不在 keyring；
//   - 未知版本：envelope 前缀/domain 不被识别；
//   - AAD/hash 不匹配：GCM 认证失败或 domain/绑定字段与期望不符。
package secret

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// AADDomain 是 domain-separated 加密的域标识。每个域对应一类数据，
// 严禁跨域解密（跨域解密会因 AAD 不匹配 fail closed）。
type AADDomain string

const (
	// AADDomainDurableRequest durable 请求快照加密域（SR-16，doc 18 §11.2）。
	AADDomainDurableRequest AADDomain = "llm-gateway:durable-request:v1"
	// AADDomainDurableResult durable 最终结果加密域（SR-16，doc 18 §11.2）。
	AADDomainDurableResult AADDomain = "llm-gateway:durable-result:v1"
	// AADDomainSanitize sanitize Phase-2 真实值加密域（SC-6，doc 19 §2）。
	AADDomainSanitize AADDomain = "llm-gateway:sanitize:v1"
)

// knownAADDomains 登记全部合法域。Encrypt 拒绝未登记域，Decrypt 拒绝
// envelope 中出现未登记域（fail closed）。
var knownAADDomains = map[AADDomain]struct{}{
	AADDomainDurableRequest: {},
	AADDomainDurableResult:  {},
	AADDomainSanitize:       {},
}

// AADBinding 是绑定到密文的上下文字段（doc 18 §11.2：绑定 tenant/task/
// request hash）。任一字段不匹配即解密失败，防止密文被搬到别的租户/任务。
type AADBinding struct {
	TenantID    string
	TaskID      string
	RequestHash string
}

// fail-closed 错误。调用方必须按错误终止，不得声称已接管或降级返回明文。
var (
	// ErrAADNoKey 无密钥（keyring nil 或 kid 未知）。
	ErrAADNoKey = errors.New("secret: no key available for AAD envelope")
	// ErrAADUnknownVersion 未知 envelope 版本或未知 domain。
	ErrAADUnknownVersion = errors.New("secret: unknown AAD envelope version or domain")
	// ErrAADMismatch AAD/hash 不匹配或密文被篡改。
	ErrAADMismatch = errors.New("secret: AAD mismatch or tampered ciphertext")
)

const (
	aadEnvelopeVersion = "v2"
	aadSep             = "|"
)

// BuildAAD 返回 domain + 绑定字段的规范编码。每个字段用
// "<len>:<value>;" 前缀，保证 ("ab","c") 与 ("a","bc") 产生不同 AAD。
func BuildAAD(domain AADDomain, b AADBinding) []byte {
	var buf bytes.Buffer
	writeField := func(s string) {
		buf.WriteString(strconv.Itoa(len(s)))
		buf.WriteByte(':')
		buf.WriteString(s)
		buf.WriteByte(';')
	}
	writeField(string(domain))
	writeField(b.TenantID)
	writeField(b.TaskID)
	writeField(b.RequestHash)
	return buf.Bytes()
}

// EncryptWithAAD 用 keyring 当前 kid 做 AES-256-GCM 加密，AAD 由 domain
// 与绑定字段构造。返回 v2 envelope 与实际使用的 key_id（doc 18 §11.2：
// envelope 需记录 snapshot_version/encryption_key_id，key_id 由调用方落列）。
func EncryptWithAAD(plaintext []byte, kr *Keyring, domain AADDomain, b AADBinding) (envelope string, keyID string, err error) {
	if kr == nil {
		return "", "", ErrAADNoKey
	}
	if _, ok := knownAADDomains[domain]; !ok {
		return "", "", fmt.Errorf("%w: domain %q", ErrAADUnknownVersion, domain)
	}

	kid := kr.current
	if strings.ContainsAny(kid, aadSep) {
		return "", "", fmt.Errorf("secret: kid %q must not contain %q", kid, aadSep)
	}
	rawKey := kr.keys[kid]

	nonce := make([]byte, gcmNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", fmt.Errorf("nonce: %w", err)
	}
	block, err := aes.NewCipher(rawKey[:])
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	blob := gcm.Seal(nonce, nonce, plaintext, BuildAAD(domain, b))
	payload := base64.RawURLEncoding.EncodeToString(blob)
	return strings.Join([]string{aadEnvelopeVersion, string(domain), kid, payload}, aadSep), kid, nil
}

// DecryptWithAAD 解开 v2 envelope。expectedDomain 由调用方给出：envelope
// 中的 domain 与期望不符即 ErrAADMismatch（跨域搬运 fail closed）。
// 无密钥 / 未知版本 / AAD 不匹配分别返回对应哨兵错误，调用方必须 fail closed。
func DecryptWithAAD(envelope string, kr *Keyring, expectedDomain AADDomain, b AADBinding) (plaintext []byte, keyID string, err error) {
	if kr == nil {
		return nil, "", ErrAADNoKey
	}
	if _, ok := knownAADDomains[expectedDomain]; !ok {
		return nil, "", fmt.Errorf("%w: domain %q", ErrAADUnknownVersion, expectedDomain)
	}

	parts := strings.SplitN(envelope, aadSep, 4)
	if len(parts) != 4 || parts[0] != aadEnvelopeVersion {
		return nil, "", fmt.Errorf("%w: malformed envelope", ErrAADUnknownVersion)
	}
	domain := AADDomain(parts[1])
	kid, payloadB64 := parts[2], parts[3]
	if _, ok := knownAADDomains[domain]; !ok {
		return nil, "", fmt.Errorf("%w: domain %q", ErrAADUnknownVersion, domain)
	}
	if domain != expectedDomain {
		return nil, "", fmt.Errorf("%w: envelope domain %q, expected %q", ErrAADMismatch, domain, expectedDomain)
	}

	rawKey, err := kr.getKey(kid)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrAADNoKey, err)
	}
	blob, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, "", fmt.Errorf("%w: base64url decode: %v", ErrAADUnknownVersion, err)
	}
	if len(blob) < gcmNonceSize+16 {
		return nil, "", fmt.Errorf("%w: payload too short", ErrAADUnknownVersion)
	}
	nonce := blob[:gcmNonceSize]
	ctAndTag := blob[gcmNonceSize:]
	block, err := aes.NewCipher(rawKey[:])
	if err != nil {
		return nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	pt, err := gcm.Open(nil, nonce, ctAndTag, BuildAAD(domain, b))
	if err != nil {
		return nil, "", ErrAADMismatch
	}
	return pt, kid, nil
}
