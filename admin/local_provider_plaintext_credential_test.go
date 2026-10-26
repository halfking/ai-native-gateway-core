package admin

// local_provider_plaintext_credential_test.go — 2026-10-26 多模态标准接口轮。
//
// 锁定：启动期 catalog 播种历史上把本地供应商占位 key 明文写进
// secret_ciphertext（ensureLocalCredentialsForSeededProviders 无加密设施的
// 旧实现），decryptCred 必须容忍这一**已知占位常量**的明文形态——否则
// refresh-models / credential check 在解密步 1ms 内失败（211 实测
// "credential decrypt failed"）。播种路径现已走真加密；本测试兜住全部存量行。

import (
	"testing"
)

func TestDecryptCredToleratesPlaintextLocalPlaceholder(t *testing.T) {
	h := NewHandler(nil, "unit-test-secret-key", nil)
	pt, isLegacy, err := h.decryptCred(localNoKeyPlaceholder)
	if err != nil {
		t.Fatalf("plaintext placeholder must pass through, got error: %v", err)
	}
	if pt != localNoKeyPlaceholder {
		t.Fatalf("plaintext placeholder must round-trip verbatim, got %q", pt)
	}
	if isLegacy {
		t.Fatalf("plaintext placeholder is not a legacy envelope")
	}
	// 反向：真密文（哪怕是错钥）不得被误判为占位明文。
	if _, _, err := h.decryptCred("gAAAAABnotarealtoken=="); err == nil {
		t.Fatalf("garbage ciphertext must still fail decryption (tolerance is exact-match only)")
	}
}
