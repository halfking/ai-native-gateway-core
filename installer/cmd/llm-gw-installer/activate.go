package main

import (
	"fmt"
	"os"

	"github.com/kaixuan/llm-gateway-go/installer/internal/activation"
)

const version = "v1.13.0"

func runActivate(mode, email, licenseKey, licensePath, masterURL string, agree bool) error {
	if mode == "" {
		return fmt.Errorf("必须指定 --mode 参数")
	}

	client := activation.NewClient(masterURL)

	switch mode {
	case "trial":
		return handleTrial(client, email, agree)
	case "key":
		return handleOnlineActivation(client, licenseKey)
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

// handleOnlineActivation 处理在线激活
func handleOnlineActivation(client *activation.Client, licenseKey string) error {
	if licenseKey == "" {
		return fmt.Errorf("-license-key 参数不能为空")
	}

	fmt.Println("▶ 在线激活 ...")

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

	// 调用在线激活 API
	resp, err := client.ActivateOnline(licenseKey, hardwareHash, instanceID, deviceName, version)
	if err != nil {
		return fmt.Errorf("激活失败: %w", err)
	}

	fmt.Printf("✅ 在线激活成功\n")
	fmt.Printf("   过期时间: %s\n", resp.ExpiresAt)

	// 写入 license.dat 到 /var/lib/kx-gateway/
	licenseDir := "/var/lib/kx-gateway"
	if err := os.MkdirAll(licenseDir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	licensePath := licenseDir + "/license.dat"
	if err := os.WriteFile(licensePath, []byte(resp.SignedLicense), 0600); err != nil {
		return fmt.Errorf("写入 license 失败: %w", err)
	}

	fmt.Printf("   License 已写入: %s\n", licensePath)

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

// handleOfflineImport 处理离线 license 导入
func handleOfflineImport(licensePath string) error {
	fmt.Printf("▶ 导入离线 License (%s) ...\n", licensePath)

	if err := activation.ImportOfflineLicense(licensePath); err != nil {
		return fmt.Errorf("导入失败: %w", err)
	}

	fmt.Println("✅ 离线 License 导入成功")
	fmt.Println("   目标路径: /var/lib/kx-gateway/license.dat")

	return nil
}
