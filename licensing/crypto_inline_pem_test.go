package licensing

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

// genAuditPKIXPEM 生成一把测试公钥的 PEM（PKIX 形态），返回 (pem 多行原文, *rsa.PublicKey)。
func genAuditPKIXPEM(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), &priv.PublicKey
}

// TestLoadPublicKeyFromInlinePEMForms 钉住 2026-11-11（48h 审计 · 154 实测）：
// systemd EnvironmentFile 不接受含真实换行的赋值 ⇒ 154 的
// LLM_GATEWAY_LICENSE_PUBLIC_KEY 整段被「Ignoring invalid environment
// assignment」丢弃 ⇒ 公钥从未进入进程、license 校验被静默禁用。
// 门：多行原样 / 单行 \n 转义 / 单行 Base64 三种形态必须解析出**同一把**钥匙；
// 垃圾输入不得被 Normalize 掩盖（原样退回，让 pem.Decode 报原本的错）。
func TestLoadPublicKeyFromInlinePEMForms(t *testing.T) {
	multiline, want := genAuditPKIXPEM(t)

	t.Run("multiline passthrough", func(t *testing.T) {
		got, err := LoadPublicKeyFromPEM(multiline)
		if err != nil {
			t.Fatalf("multiline PEM must load unchanged: %v", err)
		}
		if got.N.Cmp(want.N) != 0 || got.E != want.E {
			t.Fatal("multiline PEM parsed a different key")
		}
	})

	t.Run("single-line escaped newlines", func(t *testing.T) {
		oneLine := strings.ReplaceAll(multiline, "\n", `\n`)
		if strings.Contains(oneLine, "\n") {
			t.Fatal("fixture must be single-line")
		}
		got, err := LoadPublicKeyFromPEM(oneLine)
		if err != nil {
			t.Fatalf(`single-line \n-escaped PEM must load: %v`, err)
		}
		if got.N.Cmp(want.N) != 0 || got.E != want.E {
			t.Fatal(`\n-escaped PEM parsed a different key`)
		}
	})

	t.Run("single-line base64 whole block", func(t *testing.T) {
		b64 := base64.StdEncoding.EncodeToString([]byte(multiline))
		if strings.Contains(b64, "\n") {
			t.Fatal("fixture must be single-line")
		}
		got, err := LoadPublicKeyFromPEM(b64)
		if err != nil {
			t.Fatalf("base64-wrapped PEM must load: %v", err)
		}
		if got.N.Cmp(want.N) != 0 || got.E != want.E {
			t.Fatal("base64-wrapped PEM parsed a different key")
		}
	})

	t.Run("garbage stays garbage (fail loud)", func(t *testing.T) {
		if _, err := LoadPublicKeyFromPEM("not-a-key-at-all"); err == nil {
			t.Fatal("garbage must still fail (Normalize must not mask invalid input)")
		}
		// base64 能解但解出来不是 PEM ⇒ 也必须失败（不得静默通过）。
		notPem := base64.StdEncoding.EncodeToString([]byte("hello world, not a pem"))
		if _, err := LoadPublicKeyFromPEM(notPem); err == nil {
			t.Fatal("base64 of non-PEM must fail")
		}
	})
}

// TestNormalizeInlinePEMIdentity 钉住 NormalizeInlinePEM 的形态判定本身：
// 标准多行原样返回（引用相等语义上等价即可）；空串不崩溃。
func TestNormalizeInlinePEMIdentity(t *testing.T) {
	multiline, _ := genAuditPKIXPEM(t)
	if got := NormalizeInlinePEM(multiline); got != multiline {
		t.Fatal("multiline PEM must pass through unchanged")
	}
	if got := NormalizeInlinePEM(""); got != "" {
		t.Fatal("empty input must pass through")
	}
}
