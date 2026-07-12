package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kaixuan/llm-gateway-go/installer/internal/upgrader"
	"github.com/spf13/cobra"
)

// upgradeCmd 升级子命令
func upgradeCmd() *cobra.Command {
	var (
		action         string
		masterURL      string
		channel        string
		offline        bool
		offlinePackage string
		dataDir        string
	)

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "升级管理（check / apply / rollback）",
		Long: `升级管理工具

支持三种操作模式：
  check    - 检查是否有可用更新
  apply    - 应用更新（在线或离线）
  rollback - 回滚到上一个版本

示例：
  llm-gw-installer upgrade --action check
  llm-gw-installer upgrade --action apply --channel stable
  llm-gw-installer upgrade --action apply --offline --offline-package /path/to/package.tar.gz
  llm-gw-installer upgrade --action rollback`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if action == "" {
				return fmt.Errorf("--action 参数必填（check / apply / rollback）")
			}
			return runUpgradeAction(action, masterURL, channel, offline, offlinePackage, dataDir)
		},
	}

	cmd.Flags().StringVar(&action, "action", "", "操作: check / apply / rollback (必填)")
	cmd.Flags().StringVar(&masterURL, "master-url", "https://llm.kxpms.cn", "主控端地址")
	cmd.Flags().StringVar(&channel, "channel", "stable", "更新通道: stable / beta / nightly")
	cmd.Flags().BoolVar(&offline, "offline", false, "离线模式")
	cmd.Flags().StringVar(&offlinePackage, "offline-package", "", "离线包路径（仅 offline 模式）")
	cmd.Flags().StringVar(&dataDir, "data-dir", "/opt/llm-gateway-go", "数据目录")

	return cmd
}

// runUpgradeAction 执行升级操作
func runUpgradeAction(action, masterURL, channel string, offline bool, offlinePackage, dataDir string) error {
	// 获取当前版本
	currentVersion := getCurrentVersionFromDataDir(dataDir)

	switch action {
	case "check":
		return runUpgradeCheck(masterURL, currentVersion, channel)

	case "apply":
		if offline {
			if offlinePackage == "" {
				return fmt.Errorf("--offline 模式需要 --offline-package 参数")
			}
			// 离线升级
			backupDir := filepath.Join(dataDir, "backups", "upgrades")
			composeFile := filepath.Join(dataDir, "compose.yml")
			containerName := "kx-citus"

			applier := upgrader.NewOfflineApplier(dataDir, backupDir, composeFile, containerName)
			return applier.ApplyOfflinePackage(offlinePackage, dataDir)
		}
		return runUpgradeApply(masterURL, currentVersion, channel, dataDir)

	case "rollback":
		return upgrader.Rollback(dataDir, masterURL)

	default:
		return fmt.Errorf("未知操作: %s（支持: check / apply / rollback）", action)
	}
}

// runUpgradeCheck 检查更新
func runUpgradeCheck(masterURL, currentVersion, channel string) error {
	fmt.Printf("▶ 检查更新 ...\n")
	fmt.Printf("  当前版本: %s\n", currentVersion)
	fmt.Printf("  更新通道: %s\n", channel)
	fmt.Printf("  主控端:   %s\n", masterURL)

	release, err := upgrader.CheckUpdate(masterURL, currentVersion, channel)
	if err != nil {
		return fmt.Errorf("检查更新失败: %w", err)
	}

	if release == nil {
		fmt.Println("\n✅ 已是最新版本")
		return nil
	}

	fmt.Println("\n🎉 有可用更新:")
	fmt.Printf("  版本:     %s\n", release.Version)
	fmt.Printf("  强制更新: %v\n", release.Mandatory)
	if release.Changelog != "" {
		fmt.Printf("  更新日志:\n%s\n", release.Changelog)
	}
	fmt.Println("\n执行升级:")
	fmt.Printf("  ./llm-gw-installer upgrade --action apply --channel %s\n", channel)

	return nil
}

// runUpgradeApply 应用更新
func runUpgradeApply(masterURL, currentVersion, channel, dataDir string) error {
	fmt.Printf("▶ 检查更新 ...\n")
	fmt.Printf("  当前版本: %s\n", currentVersion)

	release, err := upgrader.CheckUpdate(masterURL, currentVersion, channel)
	if err != nil {
		return fmt.Errorf("检查更新失败: %w", err)
	}

	if release == nil {
		fmt.Println("\n✅ 已是最新版本，无需升级")
		return nil
	}

	fmt.Printf("\n▶ 准备升级到 %s ...\n", release.Version)

	if err := upgrader.ApplyUpdate(release, dataDir, masterURL); err != nil {
		return fmt.Errorf("升级失败: %w", err)
	}

	return nil
}

// getCurrentVersionFromDataDir 获取当前版本
func getCurrentVersionFromDataDir(dataDir string) string {
	versionFile := fmt.Sprintf("%s/app/VERSION", dataDir)
	data, err := os.ReadFile(versionFile)
	if err != nil {
		return "unknown"
	}
	// 去掉换行符
	version := string(data)
	if len(version) > 0 && version[len(version)-1] == '\n' {
		version = version[:len(version)-1]
	}
	return version
}
