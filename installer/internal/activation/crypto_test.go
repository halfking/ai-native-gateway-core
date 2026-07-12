package activation

import (
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	licenseKey := "LIC-test-key-123456"
	plaintext := []byte("Hello, World!")

	// 加密
	encrypted, err := EncryptOfflineRequest(licenseKey, plaintext)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	if encrypted == "" {
		t.Fatal("加密结果为空")
	}

	// 解密
	decrypted, err := DecryptOfflineLicense(licenseKey, encrypted)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Fatalf("解密结果不匹配: got %s, want %s", string(decrypted), string(plaintext))
	}
}

func TestEncryptDecryptWrongKey(t *testing.T) {
	licenseKey := "LIC-test-key-123456"
	wrongKey := "LIC-wrong-key-654321"
	plaintext := []byte("Secret Message")

	// 使用正确密钥加密
	encrypted, err := EncryptOfflineRequest(licenseKey, plaintext)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	// 使用错误密钥解密
	_, err = DecryptOfflineLicense(wrongKey, encrypted)
	if err == nil {
		t.Fatal("使用错误密钥解密应该失败")
	}
}

func TestMarshalUnmarshalOfflineRequest(t *testing.T) {
	payload := &OfflineRequestPayload{
		LicenseKey:   "LIC-test-123",
		HardwareHash: "abc123def456",
		InstanceID:   "uuid-1234-5678",
		DeviceName:   "test-device",
		Timestamp:    "2026-07-12T10:00:00Z",
	}

	// 序列化并加密
	encrypted, err := MarshalOfflineRequest(payload)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}

	// 解密并反序列化
	decrypted, err := UnmarshalOfflineRequest(payload.LicenseKey, encrypted)
	if err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}

	if decrypted.LicenseKey != payload.LicenseKey {
		t.Errorf("LicenseKey 不匹配: got %s, want %s", decrypted.LicenseKey, payload.LicenseKey)
	}
	if decrypted.HardwareHash != payload.HardwareHash {
		t.Errorf("HardwareHash 不匹配: got %s, want %s", decrypted.HardwareHash, payload.HardwareHash)
	}
	if decrypted.InstanceID != payload.InstanceID {
		t.Errorf("InstanceID 不匹配: got %s, want %s", decrypted.InstanceID, payload.InstanceID)
	}
	if decrypted.DeviceName != payload.DeviceName {
		t.Errorf("DeviceName 不匹配: got %s, want %s", decrypted.DeviceName, payload.DeviceName)
	}
}

func TestGetHardwareHash(t *testing.T) {
	hash, err := GetHardwareHash()
	if err != nil {
		t.Fatalf("生成硬件指纹失败: %v", err)
	}

	if hash == "" {
		t.Fatal("硬件指纹为空")
	}

	if len(hash) != 32 { // 16 bytes hex = 32 chars
		t.Errorf("硬件指纹长度不正确: got %d, want 32", len(hash))
	}

	// 验证稳定性：多次调用应返回相同结果
	hash2, err := GetHardwareHash()
	if err != nil {
		t.Fatalf("第二次生成失败: %v", err)
	}

	if hash != hash2 {
		t.Errorf("硬件指纹不稳定: %s != %s", hash, hash2)
	}
}

func TestGetDeviceName(t *testing.T) {
	name := GetDeviceName()
	if name == "" {
		t.Fatal("设备名为空")
	}
	if name == "unknown" {
		t.Skip("主机名获取失败（测试环境可能限制）")
	}
}
