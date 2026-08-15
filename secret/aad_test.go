package secret

import (
	"errors"
	"bytes"
	"strings"
	"testing"
)

// testKeyring 构造一个双 kid 的测试 keyring，验证加解密返回的 keyID。
func testKeyring(t *testing.T) *Keyring {
	t.Helper()
	k1 := [32]byte{1}
	k2 := [32]byte{2}
	kr, err := NewKeyring(map[string][32]byte{"k1": k1, "k2": k2}, "k2")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return kr
}

// TestEncryptWithAAD_RoundTrip：doc 18 §11.2 — domain-separated
// EncryptWithAAD/DecryptWithAAD 必须往返成功并返回实际 key_id。
func TestEncryptWithAAD_RoundTrip(t *testing.T) {
	kr := testKeyring(t)
	b := AADBinding{TenantID: "tenant-a", TaskID: "task-1", RequestHash: "hash-1"}

	env, keyID, err := EncryptWithAAD([]byte("hello durable"), kr, AADDomainDurableRequest, b)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if keyID != "k2" {
		t.Fatalf("keyID = %q, want current kid %q", keyID, "k2")
	}
	if !strings.HasPrefix(env, "v2|") {
		t.Fatalf("envelope = %q, want v2| prefix", env)
	}

	pt, gotKeyID, err := DecryptWithAAD(env, kr, AADDomainDurableRequest, b)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(pt, []byte("hello durable")) {
		t.Fatalf("plaintext = %q", pt)
	}
	if gotKeyID != "k2" {
		t.Fatalf("decrypt keyID = %q, want k2", gotKeyID)
	}
}

// TestDecryptWithAAD_FailClosed：doc 18 §11.2 门禁三条件 — 无密钥、
// 未知版本、AAD/hash 不匹配必须 fail closed（哨兵错误），绝不返回明文。
func TestDecryptWithAAD_FailClosed(t *testing.T) {
	kr := testKeyring(t)
	ok := AADBinding{TenantID: "tenant-a", TaskID: "task-1", RequestHash: "hash-1"}
	env, _, err := EncryptWithAAD([]byte("secret"), kr, AADDomainDurableResult, ok)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	cases := []struct {
		name    string
		env     string
		kr      *Keyring
		domain  AADDomain
		binding AADBinding
		wantErr error
	}{
		{"nil keyring", env, nil, AADDomainDurableResult, ok, ErrAADNoKey},
		{"unknown kid", "v2|llm-gateway:durable-result:v1|gone|AAAA", kr, AADDomainDurableResult, ok, ErrAADNoKey},
		{"unknown envelope version", "v3|llm-gateway:durable-result:v1|k2|AAAA", kr, AADDomainDurableResult, ok, ErrAADUnknownVersion},
		{"unknown domain in envelope", "v2|llm-gateway:evil:v9|k2|AAAA", kr, AADDomainDurableResult, ok, ErrAADUnknownVersion},
		{"malformed envelope", "garbage", kr, AADDomainDurableResult, ok, ErrAADUnknownVersion},
		{"empty envelope", "", kr, AADDomainDurableResult, ok, ErrAADUnknownVersion},
		{"payload too short", "v2|llm-gateway:durable-result:v1|k2|AAAA", kr, AADDomainDurableResult, ok, ErrAADUnknownVersion},
		{"tampered ciphertext", tamperLastChar(env), kr, AADDomainDurableResult, ok, ErrAADMismatch},
		{"wrong expected domain (cross-domain)", env, kr, AADDomainDurableRequest, ok, ErrAADMismatch},
		{"wrong expected domain (sanitize)", env, kr, AADDomainSanitize, ok, ErrAADMismatch},
		{"binding tenant mismatch", env, kr, AADDomainDurableResult, AADBinding{TenantID: "tenant-b", TaskID: "task-1", RequestHash: "hash-1"}, ErrAADMismatch},
		{"binding task mismatch", env, kr, AADDomainDurableResult, AADBinding{TenantID: "tenant-a", TaskID: "task-2", RequestHash: "hash-1"}, ErrAADMismatch},
		{"binding hash mismatch", env, kr, AADDomainDurableResult, AADBinding{TenantID: "tenant-a", TaskID: "task-1", RequestHash: "hash-2"}, ErrAADMismatch},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pt, _, err := DecryptWithAAD(tc.env, tc.kr, tc.domain, tc.binding)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if pt != nil {
				t.Fatalf("fail-closed must not return plaintext, got %q", pt)
			}
		})
	}
}

// tamperLastChar 篡改 base64 payload 的最后一个字符（保持 base64 合法）。
func tamperLastChar(s string) string {
	b := []byte(s)
	// raw base64 末字符低 2 位是无效填充位；翻转 bit4（位于有效位内）
	// 保证解码后的字节确实变化，且仍是合法 base64url 字符。
	b[len(b)-1] ^= 16
	return string(b)
}

// TestEncryptWithAAD_SeparatedFromCredentialEnvelope：doc 18 §11.2 —
// 现有固定 `llm-gateway:credential` AAD 的 API 不可用于 AAD envelope，
// 反之亦然（双向不可解）。
func TestEncryptWithAAD_SeparatedFromCredentialEnvelope(t *testing.T) {
	kr := testKeyring(t)
	b := AADBinding{TenantID: "t", TaskID: "k", RequestHash: "h"}

	// credential envelope 不能被 DecryptWithAAD 解开
	cred, err := EncryptAESGCM([]byte("cred"), kr)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}
	if _, _, err := DecryptWithAAD(cred, kr, AADDomainDurableRequest, b); err == nil {
		t.Fatal("credential envelope must not decrypt via DecryptWithAAD")
	}

	// AAD envelope 不能被 DecryptAESGCM 解开
	aad, _, err := EncryptWithAAD([]byte("durable"), kr, AADDomainDurableRequest, b)
	if err != nil {
		t.Fatalf("EncryptWithAAD: %v", err)
	}
	if _, err := DecryptAESGCM(aad, kr); err == nil {
		t.Fatal("AAD envelope must not decrypt via DecryptAESGCM")
	}
}

// TestBuildAAD_LengthPrefixedCanonical：长度前缀编码必须消除字段边界
// 歧义 —— ("ab","c") 与 ("a","bc") 的 AAD 不同。
func TestBuildAAD_LengthPrefixedCanonical(t *testing.T) {
	a := BuildAAD(AADDomainDurableRequest, AADBinding{TenantID: "ab", TaskID: "c"})
	b2 := BuildAAD(AADDomainDurableRequest, AADBinding{TenantID: "a", TaskID: "bc"})
	if bytes.Equal(a, b2) {
		t.Fatalf("length-prefixed AAD must be unambiguous: %q == %q", a, b2)
	}
	if !bytes.Contains(a, []byte(AADDomainDurableRequest)) {
		t.Fatalf("AAD must contain domain, got %q", a)
	}
}

// TestEncryptWithAAD_UniqueNonce：同一明文两次加密必须产生不同密文
// （随机 nonce），且均可解回。
func TestEncryptWithAAD_UniqueNonce(t *testing.T) {
	kr := testKeyring(t)
	b := AADBinding{TenantID: "t", TaskID: "k", RequestHash: "h"}
	e1, _, err := EncryptWithAAD([]byte("same"), kr, AADDomainSanitize, b)
	if err != nil {
		t.Fatalf("encrypt 1: %v", err)
	}
	e2, _, err := EncryptWithAAD([]byte("same"), kr, AADDomainSanitize, b)
	if err != nil {
		t.Fatalf("encrypt 2: %v", err)
	}
	if e1 == e2 {
		t.Fatal("two encryptions must differ (random nonce)")
	}
	for i, e := range []string{e1, e2} {
		pt, _, err := DecryptWithAAD(e, kr, AADDomainSanitize, b)
		if err != nil || string(pt) != "same" {
			t.Fatalf("envelope %d: decrypt err=%v pt=%q", i, err, pt)
		}
	}
}

// TestEncryptWithAAD_RejectsUnknownDomain：加密侧也必须拒绝未登记域。
func TestEncryptWithAAD_RejectsUnknownDomain(t *testing.T) {
	kr := testKeyring(t)
	b := AADBinding{TenantID: "t", TaskID: "k", RequestHash: "h"}
	if _, _, err := EncryptWithAAD([]byte("x"), kr, AADDomain("llm-gateway:oops:v1"), b); !errors.Is(err, ErrAADUnknownVersion) {
		t.Fatalf("err = %v, want ErrAADUnknownVersion", err)
	}
	if _, _, err := EncryptWithAAD([]byte("x"), nil, AADDomainDurableRequest, b); !errors.Is(err, ErrAADNoKey) {
		t.Fatalf("err = %v, want ErrAADNoKey", err)
	}
}
