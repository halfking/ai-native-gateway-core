package activation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetOrCreateInstanceID(t *testing.T) {
	// 备份原始的 home 目录
	originalHome := os.Getenv("HOME")
	defer os.Setenv("HOME", originalHome)

	// 使用临时目录
	tmpDir, err := os.MkdirTemp("", "test-instance-")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	os.Setenv("HOME", tmpDir)

	// 第一次调用：生成新的 instance_id
	id1, err := GetOrCreateInstanceID()
	if err != nil {
		t.Fatalf("生成 instance_id 失败: %v", err)
	}

	if id1 == "" {
		t.Fatal("instance_id 为空")
	}

	// 验证文件已创建
	instanceFile := filepath.Join(tmpDir, ".kx-gateway", "instance.id")
	if _, err := os.Stat(instanceFile); os.IsNotExist(err) {
		t.Fatal("instance.id 文件未创建")
	}

	// 第二次调用：应返回相同的 instance_id
	id2, err := GetOrCreateInstanceID()
	if err != nil {
		t.Fatalf("第二次获取 instance_id 失败: %v", err)
	}

	if id1 != id2 {
		t.Errorf("instance_id 不稳定: %s != %s", id1, id2)
	}
}

func TestGenerateOfflineRequest(t *testing.T) {
	// 使用临时目录
	tmpDir, err := os.MkdirTemp("", "test-offline-")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 切换到临时目录
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	licenseKey := "LIC-test-123"
	hardwareHash := "abc123"
	instanceID := "uuid-1234"
	deviceName := "test-device"

	reqPath, err := GenerateOfflineRequest(licenseKey, hardwareHash, instanceID, deviceName)
	if err != nil {
		t.Fatalf("生成离线请求失败: %v", err)
	}

	if reqPath != "activation.req" {
		t.Errorf("请求文件路径错误: got %s, want activation.req", reqPath)
	}

	// 验证文件存在
	if _, err := os.Stat(reqPath); os.IsNotExist(err) {
		t.Fatal("activation.req 文件未创建")
	}

	// 验证文件内容可以解密
	data, err := os.ReadFile(reqPath)
	if err != nil {
		t.Fatalf("读取请求文件失败: %v", err)
	}

	payload, err := UnmarshalOfflineRequest(licenseKey, string(data))
	if err != nil {
		t.Fatalf("解密请求失败: %v", err)
	}

	if payload.LicenseKey != licenseKey {
		t.Errorf("LicenseKey 不匹配: got %s, want %s", payload.LicenseKey, licenseKey)
	}
	if payload.HardwareHash != hardwareHash {
		t.Errorf("HardwareHash 不匹配: got %s, want %s", payload.HardwareHash, hardwareHash)
	}
}

func TestImportOfflineLicense(t *testing.T) {
	// 使用临时目录作为源文件
	tmpDir, err := os.MkdirTemp("", "test-import-")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建测试 license 文件
	testLicense := "test-license-content-xyz"
	licensePath := filepath.Join(tmpDir, "test-license.dat")
	if err := os.WriteFile(licensePath, []byte(testLicense), 0600); err != nil {
		t.Fatalf("创建测试 license 文件失败: %v", err)
	}

	// 临时替换目标目录（在真实场景中是 /var/lib/kx-gateway）
	// 这里我们跳过实际导入测试，因为需要 root 权限
	t.Skip("跳过导入测试（需要 /var/lib/kx-gateway 写权限）")
}
