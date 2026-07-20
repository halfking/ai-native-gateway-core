package pluginruntime

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
)

// VerifyManifestSignature 校验 <manifestPath>.sig 是 manifest 内容的 ed25519 签名。
// pubkeyHex 是 ed25519 公钥的 hex 编码（来自 env LLM_GATEWAY_PLUGIN_SIGNING_PUBKEY）。
// pubkeyHex 为空时跳过校验（开发模式，返回 nil）。
// 这是离线最小闭环：完整签名链（maintain 签发 + activation）由分发激活模块提供。
func VerifyManifestSignature(manifestPath, pubkeyHex string) error {
	if pubkeyHex == "" {
		return nil
	}
	pub, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return fmt.Errorf("invalid signing pubkey hex: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid signing pubkey size: got %d want %d", len(pub), ed25519.PublicKeySize)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest for verify: %w", err)
	}
	sig, err := os.ReadFile(manifestPath + ".sig")
	if err != nil {
		return fmt.Errorf("read manifest signature (missing .sig file): %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), manifestBytes, sig) {
		return fmt.Errorf("manifest signature verification failed")
	}
	return nil
}
