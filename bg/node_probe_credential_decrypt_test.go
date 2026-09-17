package bg

import "testing"

// R40 (2026-09-18, 闭合 R39 §三#3)：credentialSpecificDecryptFailure 把
// gateway-side guard 的压制面细分为"实例错 key"（保持压制 + 熔断/sweep
// 兜底）与"单凭据密文永久损坏"（跟随凭据的真实不可用信号，豁免压制）。
func TestCredentialSpecificDecryptFailure(t *testing.T) {
	decryptDetail := "build endpoint failed: decrypt: cannot decrypt: unknown format (cred_id=17, model=gpt-x)"
	if !isDecryptShapedProbeDetail(decryptDetail) {
		t.Fatalf("decrypt-shaped detail must classify: %q", decryptDetail)
	}
	if isDecryptShapedProbeDetail("build endpoint failed: no rows in result set: credential_id=17") {
		t.Fatal("missing-binding detail must not classify as decrypt-shaped")
	}
	if isDecryptShapedProbeDetail("") {
		t.Fatal("empty detail must not classify as decrypt-shaped")
	}

	w := &NodeProbeWorker{}
	if !w.credentialSpecificDecryptFailure(decryptDetail) {
		t.Fatal("decrypt failure below the circuit threshold must be treated as credential-specific (exempt)")
	}

	for i := 0; i < decryptTripThreshold; i++ {
		w.recordDecryptFailure()
	}
	if w.credentialSpecificDecryptFailure(decryptDetail) {
		t.Fatal("once the instance circuit has tripped, suppression must hold (instance-wide key problem)")
	}

	w.resetDecryptFailures()
	if w.credentialSpecificDecryptFailure("build endpoint failed: no rows in result set") {
		t.Fatal("non-decrypt gateway-side errors must never be exempted")
	}
}
