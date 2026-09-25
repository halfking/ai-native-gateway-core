package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/activation"
	"github.com/kaixuan/llm-gateway-go/installer/internal/enrollment"
)

const version = "v1.13.0"

func runActivate(mode, email, licenseKey, licensePath, masterURL string, agree bool) error {
	if mode == "" {
		return fmt.Errorf("必须指定 --mode 参数")
	}

	switch mode {
	case "trial":
		return handleTrial(activation.NewClient(masterURL), email, agree)
	case "key":
		return handleOnlineActivation(masterURL, licenseKey)
	case "offline-request":
		return handleOfflineRequest(licenseKey)
	case "offline-import":
		return handleOfflineImport(licensePath)
	default:
		return fmt.Errorf("未知模式: %s", mode)
	}
}

// handleTrial 处理试用申请
func handleTrial(client *activation.Client, email string, agree bool) error {
	if email == "" {
		return fmt.Errorf("-email 参数不能为空")
	}
	if !agree {
		return fmt.Errorf("必须使用 --agree 同意服务条款和隐私政策")
	}

	fmt.Printf("▶ 申请试用 License (%s) ...\n", email)

	resp, err := client.RequestTrial(email, agree)
	if err != nil {
		return fmt.Errorf("申请失败: %w", err)
	}

	fmt.Printf("✅ 试用 License 申请成功\n")
	fmt.Printf("   License Key: %s\n", resp.LicenseKey)
	fmt.Printf("   过期时间: %s\n", resp.ExpiresAt)
	fmt.Printf("   消息: %s\n", resp.Message)

	return nil
}

// handleOnlineActivation 处理在线激活。
// 主控端契约（cmd/license-authority/register_handler.go）：register 的新设备
// 分支把 license_key_hash 当完整 license key 查库，查得即同调用激活设备 ——
// 即 register 即激活；主控不存在 /api/v1/license/activate 端点。
// 在线模式下 license 由 DB 权威管理，无需落 license.dat（那是离线导入路径的产物）。
func handleOnlineActivation(masterURL, licenseKey string) error {
	if licenseKey == "" {
		return fmt.Errorf("-license-key 参数不能为空")
	}

	fmt.Println("▶ 在线激活（注册即激活）...")

	// 获取或创建 instance_id
	instanceID, err := activation.GetOrCreateInstanceID()
	if err != nil {
		return fmt.Errorf("获取 instance_id 失败: %w", err)
	}
	fmt.Printf("   Instance ID: %s\n", instanceID)

	// 生成硬件指纹
	hardwareHash, err := activation.GetHardwareHash()
	if err != nil {
		return fmt.Errorf("生成硬件指纹失败: %w", err)
	}
	fmt.Printf("   Hardware Hash: %s\n", hardwareHash)

	deviceName := activation.GetDeviceName()
	fmt.Printf("   Device Name: %s\n", deviceName)

	// ed25519 keypair（私钥留 ~/.kx-gateway，重复激活公钥稳定）
	publicKey, err := activation.EnsureHomeKeypair()
	if err != nil {
		return fmt.Errorf("生成设备密钥对失败: %w", err)
	}

	// register：license_key_hash 携带完整 license key
	client := enrollment.NewClient(masterURL)
	regResp, err := client.Register(context.Background(), enrollment.RegisterRequest{
		InstanceID:     instanceID,
		InstanceType:   "standalone",
		Hostname:       deviceName,
		Version:        version,
		HardwareHash:   hardwareHash,
		LicenseKeyHash: licenseKey,
		PublicKey:      publicKey,
	})
	if err != nil {
		return fmt.Errorf("激活失败: %w", err)
	}

	// 注册成功：instance_token 必须落 ~/.kx-gateway/instance.token（0600），
	// 否则 heartbeat 子命令永远读不到凭据（"请先执行 activate"死循环）。
	if regResp == nil || regResp.InstanceToken == "" {
		return fmt.Errorf("激活响应缺少 instance_token（master=%s），凭据不完整", masterURL)
	}
	if err := activation.WriteHomeInstanceToken(regResp.InstanceToken); err != nil {
		return fmt.Errorf("写入 instance_token 失败: %w", err)
	}

	fmt.Printf("✅ 在线激活成功\n")
	if regResp != nil && !regResp.ExpiresAt.IsZero() {
		fmt.Printf("   凭据过期时间: %s\n", regResp.ExpiresAt.Format(time.RFC3339))
	}
	fmt.Println("   instance_token 已写入 ~/.kx-gateway/instance.token（heartbeat 命令可直接使用）")
	fmt.Println("   在线模式下 license 由主控 DB 权威管理（license.dat 仅离线模式需要）")

	return nil
}

// handleOfflineRequest 处理离线激活请求生成
func handleOfflineRequest(licenseKey string) error {
	if licenseKey == "" {
		return fmt.Errorf("-license-key 参数不能为空")
	}

	fmt.Println("▶ 生成离线激活请求 ...")

	// 获取或创建 instance_id
	instanceID, err := activation.GetOrCreateInstanceID()
	if err != nil {
		return fmt.Errorf("获取 instance_id 失败: %w", err)
	}
	fmt.Printf("   Instance ID: %s\n", instanceID)

	// 生成硬件指纹
	hardwareHash, err := activation.GetHardwareHash()
	if err != nil {
		return fmt.Errorf("生成硬件指纹失败: %w", err)
	}
	fmt.Printf("   Hardware Hash: %s\n", hardwareHash)

	deviceName := activation.GetDeviceName()
	fmt.Printf("   Device Name: %s\n", deviceName)

	// 生成加密的离线请求文件
	reqPath, err := activation.GenerateOfflineRequest(licenseKey, hardwareHash, instanceID, deviceName)
	if err != nil {
		return fmt.Errorf("生成请求失败: %w", err)
	}

	fmt.Printf("✅ 离线激活请求已生成\n")
	fmt.Printf("   文件: %s\n", reqPath)
	fmt.Println("\n请将此文件发送给主控端管理员进行离线签发。")

	return nil
}

// handleOfflineImport 处理离线 license 导入。installDir 由 resolveInstallDir
// 从环境变量推断（INSTALL_DIR / LLM_GATEWAY_INSTALL_DIR / cwd），与
// installer/internal/enrollment/heartbeat.go 和 cmd/llm-launcher/main.go
// 保持一致——wizard 在 $PWD 写入、systemd / 容器在 /var/lib/kx-gateway 写入。
func handleOfflineImport(licensePath string) error {
	installDir := resolveInstallDir()
	fmt.Printf("▶ 导入离线 License (%s) ...\n", licensePath)

	if err := activation.ImportOfflineLicense(licensePath, installDir); err != nil {
		return fmt.Errorf("导入失败: %w", err)
	}

	targetPath := filepath.Join(installDir, "license.dat")
	fmt.Println("✅ 离线 License 导入成功")
	fmt.Printf("   目标路径: %s\n", targetPath)

	return nil
}

// resolveInstallDir 解析离线 license 应写入的目标目录。查找顺序与
// installer/internal/enrollment/heartbeat.go (line 179-185) 及
// cmd/llm-launcher/main.go (resolveInstallDir) 完全一致：
//
//  1. $INSTALL_DIR              —— wizard 与 shell 导出值
//  2. $LLM_GATEWAY_INSTALL_DIR  —— systemd / 容器环境别名
//  3. os.Getwd()                —— 前台 CLI 兜底（不能为空，因为
//     ImportOfflineLicense 会因为 installDir == "" 直接失败）
//
// 末尾用 "." 兜底是为了避免在容器里 cwd 读取失败时返回空字符串触
// 发 ImportOfflineLicense 的 "installDir is required" 报错——操作员
// 期望看到的是写入了某个明确路径，而不是一个莫名其妙的配置错误。
func resolveInstallDir() string {
	if v := strings.TrimSpace(os.Getenv("INSTALL_DIR")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_INSTALL_DIR")); v != "" {
		return v
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	return "."
}
