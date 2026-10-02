package activation

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

// GenerateOfflineRequest 生成离线激活请求文件 activation.req
func GenerateOfflineRequest(licenseKey, hardwareHash, instanceID, deviceName string) (string, error) {
	payload := &OfflineRequestPayload{
		LicenseKey:   licenseKey,
		HardwareHash: hardwareHash,
		InstanceID:   instanceID,
		DeviceName:   deviceName,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}

	encrypted, err := MarshalOfflineRequest(payload)
	if err != nil {
		return "", fmt.Errorf("encrypt request: %w", err)
	}

	// 写入 activation.req
	reqPath := "activation.req"
	if err := os.WriteFile(reqPath, []byte(encrypted), 0600); err != nil {
		return "", fmt.Errorf("write activation.req: %w", err)
	}

	return reqPath, nil
}

// ImportOfflineLicense copies license.dat from licensePath into <installDir>/license.dat.
//
// The target directory is parameterized so that the wizard (which writes into the
// installer's $PWD) and a containerized launcher (which reads from /var/lib/kx-gateway
// or whatever LLM_GATEWAY_INSTALL_DIR/INSTALL_DIR resolves to) both end up writing
// to the right place. Callers MUST resolve installDir first (see resolveInstallDir
// in cmd/llm-gw-installer/activate.go); passing an empty string is a programming
// error and returns immediately — better to fail loud than to silently drop the
// license into a hard-coded /var/lib/kx-gateway that may not exist on a fresh box.
//
// File mode is 0600 — the license contains a long-lived signing key, never 0644.
func ImportOfflineLicense(licensePath, installDir string) error {
	if installDir == "" {
		return fmt.Errorf("installDir is required (use --install-dir or set INSTALL_DIR/LLM_GATEWAY_INSTALL_DIR)")
	}

	data, err := os.ReadFile(licensePath)
	if err != nil {
		return fmt.Errorf("read license file: %w", err)
	}

	if err := os.MkdirAll(installDir, 0755); err != nil {
		return fmt.Errorf("create install dir %s: %w", installDir, err)
	}

	targetPath := filepath.Join(installDir, "license.dat")
	if err := os.WriteFile(targetPath, data, 0600); err != nil {
		return fmt.Errorf("write license to %s: %w", targetPath, err)
	}

	return nil
}

// GetOrCreateInstanceID 获取或创建 instance_id
// 持久化到 ~/.kx-gateway/instance.id
func GetOrCreateInstanceID() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}

	configDir := filepath.Join(homeDir, ".kx-gateway")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}

	instanceFile := filepath.Join(configDir, "instance.id")

	// 尝试读取现有 instance_id
	if data, err := os.ReadFile(instanceFile); err == nil {
		instanceID := string(data)
		if len(instanceID) > 0 {
			return instanceID, nil
		}
	}

	// 生成新的 instance_id
	instanceID := uuid.New().String()
	if err := os.WriteFile(instanceFile, []byte(instanceID), 0600); err != nil {
		return "", fmt.Errorf("write instance.id: %w", err)
	}

	return instanceID, nil
}

// GetHardwareHash 生成硬件指纹哈希（简化版，不依赖 licensing 包）
func GetHardwareHash() (string, error) {
	// 收集硬件信息
	hostname, _ := os.Hostname()
	osArch := runtime.GOOS + "-" + runtime.GOARCH
	mac := getPrimaryMAC()

	// 生成哈希
	raw := fmt.Sprintf("%s|%s|%s", hostname, osArch, mac)
	hash := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", hash[:16]), nil
}

// getPrimaryMAC 获取主网卡 MAC 地址
func getPrimaryMAC() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		mac := iface.HardwareAddr.String()
		if strings.HasPrefix(mac, "00:00:00:00:00:00") {
			continue
		}
		return mac
	}
	return ""
}

// GetDeviceName 获取设备名称（主机名）
func GetDeviceName() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}
