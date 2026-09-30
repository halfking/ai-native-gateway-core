package config

import "testing"

// TestValidateSecretKeyDistinct 钉住 §三#6 的 Go 侧 SK≠CEK 门（2026-09-28
// §7.1 全网 503 事故形态：CEK 被贴进 SK 槽 → 凭据全部不可解密）。判定窄域：
// 仅"两者都显式设置且等值"是冲突——CEK 未设走 SK 派生 keyring 是合法回退，
// 不能一刀切禁等值。
func TestValidateSecretKeyDistinct(t *testing.T) {
	cases := []struct {
		name      string
		sk, cek   string
		wantClean bool
	}{
		{"both empty is not a collision", "", "", true},
		{"CEK unset falls back to SK-derived keyring (legal)", "sk-value", "", true},
		{"SK empty is not a collision", "", "cek-value", true},
		{"explicitly equal is the incident shape", "same-44-byte-value", "same-44-byte-value", false},
		{"different values are clean", "sk-value", "cek-value", true},
		{"whitespace-only difference still collides", " same-value ", "same-value", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{SecretKey: tc.sk, CredentialEncryptionKey: tc.cek}
			violation := cfg.ValidateSecretKeyDistinct()
			if tc.wantClean && violation != "" {
				t.Fatalf("ValidateSecretKeyDistinct() = %q, want clean", violation)
			}
			if !tc.wantClean && violation == "" {
				t.Fatal("ValidateSecretKeyDistinct() = clean, want SK==CEK violation")
			}
		})
	}
}
