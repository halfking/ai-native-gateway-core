package envdetect

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
		// 选 docker-ce 源：ubuntu 用 ubuntu 源，debian/deepin/uos 用 debian 源
		// codename 优先用 /etc/os-release 的 VERSION_CODENAME（不依赖 lsb_release）
		// fallback：debian 11→bullseye, 12→bookworm；ubuntu 22.04→jammy, 24.04→noble
		distro := "debian"
		codename := info.VersionCodename
		switch info.Distribution {
		case DistUbuntu:
			distro = "ubuntu"
			if codename == "" {
				codename = guessUbuntuCodename(info.VersionID)
			}
		default:
			if codename == "" {
				codename = guessDebianCodename(info.VersionID)
			}
		}
		if codename == "" {
			codename = "bullseye" // 兜底，避免源 URL 为空
		}

		return &InstallStrategy{
			Method:       "apt-aliyun",
			RequiresSudo: !info.HasSudo && os.Geteuid() != 0,
			Description:  fmt.Sprintf("通过 apt + 阿里云源安装 docker-ce (%s %s)", distro, codename),
			Steps: []string{
				"apt-get update -qq",
				"apt-get install -y ca-certificates curl gnupg",
				"install -m 0755 -d /etc/apt/keyrings",
				fmt.Sprintf("curl -fsSL https://mirrors.aliyun.com/docker-ce/linux/%s/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg", distro),
				fmt.Sprintf(`echo "deb [arch=$(dpkg --print-architecture)] https://mirrors.aliyun.com/docker-ce/linux/%s %s stable" > /etc/apt/sources.list.d/docker.list`, distro, codename),
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

	// Windows/macOS 不支持 sh/sudo，Execute 只服务 Linux 自动安装路径。
	// 若误入此处，明确报错而非尝试执行必然失败的 sh 命令。
	if info.Family != FamilyDebian && info.Family != FamilyRhel {
		return fmt.Errorf("自动安装仅支持 Linux (debian/rhel 系)，当前 %s 请手动安装 docker", info.Family)
	}

	// 执行每一步
	for i, step := range s.Steps {
		logger(fmt.Sprintf("  [%d/%d] %s", i+1, len(s.Steps), step))

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		// 注意：不能 defer cancel，否则循环内 ctx 累积；改为循环末尾显式 cancel

		// 一次性构造 CommandContext（避免重复构造丢失参数）
		var cmd *exec.Cmd
		if s.RequiresSudo {
			cmd = exec.CommandContext(ctx, "sudo", "sh", "-c", step)
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-c", step)
		}

		// 实时输出
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		runErr := cmd.Run()
		cancel() // 显式释放本步骤的 ctx
		if runErr != nil {
			return fmt.Errorf("步骤 [%s] 失败: %w", step, runErr)
		}
	}

	return nil
}

// guessUbuntuCodename 从 VERSION_ID 推断 ubuntu codename
// /etc/os-release 没写 VERSION_CODENAME 时用
func guessUbuntuCodename(versionID string) string {
	switch versionID {
	case "20.04":
		return "focal"
	case "22.04":
		return "jammy"
	case "24.04":
		return "noble"
	case "18.04":
		return "bionic"
	}
	return ""
}

// guessDebianCodename 从 VERSION_ID 推断 debian codename
// 也用于 Deepin / UOS（基于 debian）
func guessDebianCodename(versionID string) string {
	// 取主版本号
	major := versionID
	if idx := strings.Index(versionID, "."); idx > 0 {
		major = versionID[:idx]
	}
	switch major {
	case "10":
		return "buster"
	case "11":
		return "bullseye"
	case "12":
		return "bookworm"
	case "13":
		return "trixie"
	}
	return ""
}
