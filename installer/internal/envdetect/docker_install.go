package envdetect

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// InstallStrategy docker 安装策略
type InstallStrategy struct {
	Method       string   // apt-aliyun / dnf-aliyun / isulad / prompt-user / skip
	RequiresSudo bool
	Description  string
	Steps        []string
}

// PlanInstall 根据 OS 信息规划 docker 安装方式
func PlanInstall(info *OSInfo, hasInternet bool) *InstallStrategy {
	// macOS / Windows：不能自动装，引导用户
	if info.Family == FamilyDarwin {
		return &InstallStrategy{
			Method:      "prompt-user",
			Description: "macOS 需要手动安装 OrbStack 或 Docker Desktop",
			Steps: []string{
				"打开 https://orbstack.dev/ 下载安装 OrbStack（推荐，资源占用最低）",
				"或打开 https://www.docker.com/products/docker-desktop/ 下载安装 Docker Desktop",
				"安装完成后重跑 install.sh",
			},
		}
	}
	if info.Family == FamilyWindows {
		return &InstallStrategy{
			Method:      "prompt-user",
			Description: "Windows 需要手动安装 Docker Desktop",
			Steps: []string{
				"启用 WSL2：PowerShell (管理员) 执行 wsl --install -d Ubuntu",
				"重启后下载安装 Docker Desktop: https://www.docker.com/products/docker-desktop/",
				"在 Docker Desktop Settings → General 勾选 'Use the WSL 2 based engine'",
				"安装完成后重跑 install.ps1",
			},
		}
	}

	// openEuler 自带 iSulad
	if info.Distribution == DistOpenEuler && !hasInternet {
		return &InstallStrategy{
			Method:       "isulad",
			RequiresSudo: info.HasSudo || os.Geteuid() == 0,
			Description:  "openEuler 自带 iSulad（兼容 docker）",
			Steps: []string{
				"systemctl enable --now isulad",
			},
		}
	}

	// Debian 系（含 Deepin / UOS）
	if info.Family == FamilyDebian {
		return &InstallStrategy{
			Method:       "apt-aliyun",
			RequiresSudo: !info.HasSudo && os.Geteuid() != 0,
			Description:  "通过 apt + 阿里云源安装 docker-ce",
			Steps: []string{
				"apt-get update -qq",
				"apt-get install -y ca-certificates curl gnupg",
				"install -m 0755 -d /etc/apt/keyrings",
				"curl -fsSL https://mirrors.aliyun.com/docker-ce/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg",
				`echo "deb [arch=$(dpkg --print-architecture)] https://mirrors.aliyun.com/docker-ce/linux/ubuntu $(lsb_release -cs) stable" > /etc/apt/sources.list.d/docker.list`,
				"apt-get update -qq",
				"apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",
				"systemctl enable --now docker",
				"usermod -aG docker $USER",
			},
		}
	}

	// RHEL 系（含 CentOS / Fedora / openEuler / Kylin / Anolis / NeoKylin）
	if info.Family == FamilyRhel {
		return &InstallStrategy{
			Method:       "dnf-aliyun",
			RequiresSudo: !info.HasSudo && os.Geteuid() != 0,
			Description:  "通过 dnf + 阿里云源安装 docker-ce",
			Steps: []string{
				"dnf -y install dnf-plugins-core",
				"dnf config-manager --add-repo https://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo",
				"dnf -y install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",
				"systemctl enable --now docker",
				"usermod -aG docker $USER",
			},
		}
	}

	return &InstallStrategy{
		Method:      "skip",
		Description: "无法自动安装 docker，请手动安装后重跑",
	}
}

// Execute 执行安装步骤
func (s *InstallStrategy) Execute(info *OSInfo, logger func(string)) error {
	if s.Method == "prompt-user" {
		logger("⚠️  需要手动安装 docker:")
		for _, step := range s.Steps {
			logger("  • " + step)
		}
		return fmt.Errorf("docker 未安装")
	}
	if s.Method == "skip" {
		return fmt.Errorf("无法自动安装 docker")
	}

	// 执行每一步
	for i, step := range s.Steps {
		logger(fmt.Sprintf("  [%d/%d] %s", i+1, len(s.Steps), step))

		var cmd *exec.Cmd
		if s.RequiresSudo {
			cmd = exec.Command("sudo", "sh", "-c", step)
		} else {
			cmd = exec.Command("sh", "-c", step)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cmd = exec.CommandContext(ctx, cmd.Args[0], cmd.Args[1:]...)

		// 实时输出
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("步骤 [%s] 失败: %w", step, err)
		}
	}

	return nil
}
