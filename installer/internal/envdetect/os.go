// Package envdetect 提供环境自动感知能力：识别 OS / 架构 / 包管理器 / docker 引擎 / 网络
package envdetect

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// OSFamily 操作系统家族
type OSFamily string

const (
	FamilyDebian   OSFamily = "debian"
	FamilyRhel     OSFamily = "rhel"
	FamilySuse     OSFamily = "suse"
	FamilyDarwin   OSFamily = "darwin"
	FamilyWindows  OSFamily = "windows"
	FamilyUnknown  OSFamily = "unknown"
)

// Distribution 已知发行版
type Distribution string

const (
	DistUbuntu      Distribution = "ubuntu"
	DistDebian      Distribution = "debian"
	DistDeepin      Distribution = "deepin"
	DistUOS         Distribution = "uos"
	DistCentOS      Distribution = "centos"
	DistRHEL        Distribution = "rhel"
	DistFedora      Distribution = "fedora"
	DistOpenEuler   Distribution = "openEuler"
	DistAnolis      Distribution = "anolis"
	DistKylin       Distribution = "kylin"
	DistNeoKylin    Distribution = "neokylin"
	DistOpenCloudOS Distribution = "opencloudos"
	DistMacOS       Distribution = "macos"
	DistWindows     Distribution = "windows"
	DistUnknown     Distribution = "unknown"
)

// PackageMgr 包管理器
type PackageMgr string

const (
	PkgApt    PackageMgr = "apt"
	PkgDnf    PackageMgr = "dnf"
	PkgYum    PackageMgr = "yum"
	PkgZypper PackageMgr = "zypper"
	PkgBrew   PackageMgr = "brew"
	PkgWinget PackageMgr = "winget"
	PkgChoco  PackageMgr = "choco"
	PkgNone   PackageMgr = "none"
)

// ContainerEng 容器引擎
type ContainerEng string

const (
	EngDocker  ContainerEng = "docker"
	EngPodman  ContainerEng = "podman"
	EngISulad  ContainerEng = "isulad"
	EngNerdctl ContainerEng = "nerdctl"
	EngNone    ContainerEng = "none"
)

// OSInfo 操作系统信息
type OSInfo struct {
	Family       OSFamily
	Distribution Distribution
	Arch         string
	Kernel       string
	PackageMgr   PackageMgr
	ContainerEng ContainerEng
	DockerVersion string
	HasSudo      bool
	// VersionID 来自 /etc/os-release 的 VERSION_ID（如 "22.04" / "8.8"）
	VersionID string
	// VersionCodename 来自 /etc/os-release 的 VERSION_CODENAME（如 "jammy" / "bullseye"）
	// 国产 OS 可能留空，PlanInstall 会做 fallback
	VersionCodename string
}

// Detect 检测当前 OS 信息
func Detect() (*OSInfo, error) {
	info := &OSInfo{
		Arch:   normalizeArch(runtime.GOARCH),
		Kernel: getKernel(),
	}

	switch runtime.GOOS {
	case "linux":
		if err := detectLinux(info); err != nil {
			return nil, err
		}
	case "darwin":
		detectMacOS(info)
	case "windows":
		detectWindows(info)
	default:
		info.Family = FamilyUnknown
		info.Distribution = DistUnknown
	}

	info.ContainerEng, info.DockerVersion = probeDockerEngine()
	info.HasSudo = probeSudo()

	return info, nil
}

func detectLinux(info *OSInfo) error {
	// 读 /etc/os-release
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return fmt.Errorf("无法读取 /etc/os-release: %w", err)
	}

	id, idLike, versionID, codename := parseOSRelease(string(data))
	info.Distribution = mapDistribution(id, idLike)
	info.Family = detectFamily(id, idLike)
	info.PackageMgr = detectPackageMgr(info.Family, id)
	info.VersionID = versionID
	info.VersionCodename = codename
	return nil
}

func detectMacOS(info *OSInfo) {
	info.Family = FamilyDarwin
	info.Distribution = DistMacOS
	info.PackageMgr = PkgBrew
}

func detectWindows(info *OSInfo) {
	info.Family = FamilyWindows
	info.Distribution = DistWindows
	// Windows 包管理器：优先 winget，备选 choco
	if _, err := exec.LookPath("winget"); err == nil {
		info.PackageMgr = PkgWinget
	} else if _, err := exec.LookPath("choco"); err == nil {
		info.PackageMgr = PkgChoco
	} else {
		info.PackageMgr = PkgNone
	}
}

// parseOSRelease 解析 /etc/os-release
func parseOSRelease(content string) (id, idLike, versionID, codename string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		val := strings.Trim(parts[1], `"`)
		switch key {
		case "ID":
			id = val
		case "ID_LIKE":
			idLike = val
		case "VERSION_ID":
			versionID = val
		case "VERSION_CODENAME":
			codename = val
		}
	}
	return
}

// mapDistribution ID → Distribution
func mapDistribution(id, idLike string) Distribution {
	id = strings.ToLower(id)
	idLike = strings.ToLower(idLike)

	switch id {
	case "ubuntu":
		return DistUbuntu
	case "debian":
		return DistDebian
	case "deepin":
		return DistDeepin
	case "uos":
		return DistUOS
	case "centos":
		return DistCentOS
	case "rhel":
		return DistRHEL
	case "fedora":
		return DistFedora
	case "openeuler":
		return DistOpenEuler
	case "opencloudos":
		return DistOpenCloudOS
	case "kylin":
		return DistKylin
	case "neokylin":
		return DistNeoKylin
	}

	// 通过 ID_LIKE 推断
	if strings.Contains(idLike, "rhel") || strings.Contains(idLike, "fedora") || strings.Contains(idLike, "centos") {
		switch id {
		case "openanolis", "anolis":
			return DistAnolis
		}
	}
	if strings.Contains(idLike, "debian") {
		return DistDebian
	}

	return DistUnknown
}

// detectFamily 根据 ID_LIKE 决定 OS 家族
func detectFamily(id, idLike string) OSFamily {
	idLike = strings.ToLower(idLike)
	id = strings.ToLower(id)

	if strings.Contains(idLike, "debian") {
		return FamilyDebian
	}
	if strings.Contains(idLike, "rhel") || strings.Contains(idLike, "fedora") {
		return FamilyRhel
	}
	if strings.Contains(idLike, "suse") {
		return FamilySuse
	}
	// 单 ID 判断
	switch id {
	case "ubuntu", "debian", "deepin", "uos":
		return FamilyDebian
	case "rhel", "centos", "fedora", "openeuler", "opencloudos", "kylin", "neokylin", "openanolis", "anolis":
		return FamilyRhel
	}
	return FamilyUnknown
}

// detectPackageMgr 根据 OS 家族决定包管理器
func detectPackageMgr(family OSFamily, id string) PackageMgr {
	switch family {
	case FamilyDebian:
		return PkgApt
	case FamilyRhel:
		// Fedora/openEuler 22+ 默认 dnf；老 RHEL/CentOS 7 用 yum
		if id == "centos" || id == "rhel" {
			// 通过是否有 dnf 命令判断
			if _, err := exec.LookPath("dnf"); err == nil {
				return PkgDnf
			}
			return PkgYum
		}
		return PkgDnf
	case FamilySuse:
		return PkgZypper
	case FamilyDarwin:
		return PkgBrew
	case FamilyWindows:
		return PkgWinget
	}
	return PkgNone
}

// normalizeArch 标准化架构名
func normalizeArch(arch string) string {
	switch arch {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	case "loong64", "loongarch64":
		return "loong64"
	case "sw64", "sw_64":
		return "sw64"
	case "386":
		return "386"
	}
	return arch
}

// getKernel 获取内核版本
func getKernel() string {
	if runtime.GOOS == "windows" {
		// Windows: 用 wmic 或直接返回 GOOS
		out, err := exec.Command("cmd", "/c", "ver").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		return "windows"
	}
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return runtime.GOOS
	}
	return strings.TrimSpace(string(out))
}

// probeDockerEngine 探测容器引擎
func probeDockerEngine() (ContainerEng, string) {
	candidates := []struct {
		cmd string
		eng ContainerEng
	}{
		{"docker", EngDocker},
		{"podman", EngPodman},
		{"isulad", EngISulad},
		{"nerdctl", EngNerdctl},
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c.cmd); err == nil && path != "" {
			// 探测 daemon
			if isEngineRunning(c.cmd) {
				version := getEngineVersion(c.cmd)
				return c.eng, version
			}
		}
	}
	return EngNone, ""
}

// isEngineEngineRunning 检测 engine 是否在运行
func isEngineRunning(cmd string) bool {
	// docker: docker info / docker ps
	// podman: podman info
	// isulad: isula info
	out, err := exec.Command(cmd, "info").CombinedOutput()
	if err != nil {
		return false
	}
	return !bytes.Contains(out, []byte("Cannot connect")) &&
		!bytes.Contains(out, []byte("cannot connect")) &&
		!bytes.Contains(out, []byte("Is the docker daemon running"))
}

// getEngineVersion 获取引擎版本
func getEngineVersion(cmd string) string {
	out, err := exec.Command(cmd, "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		out, err = exec.Command(cmd, "--version").Output()
		if err != nil {
			return ""
		}
	}
	v := strings.TrimSpace(string(out))
	// 去掉前缀（如 "Docker version "、"podman version "）
	if idx := strings.LastIndex(v, " "); idx > 0 {
		v = v[idx+1:]
	}
	return v
}

// probeSudo 检测是否可以使用 sudo
func probeSudo() bool {
	if runtime.GOOS == "windows" {
		return false // Windows 上不需要 sudo
	}
	if os.Geteuid() == 0 {
		return false // 已经是 root
	}
	if _, err := exec.LookPath("sudo"); err == nil {
		// sudo -n true 不需要密码
		if err := exec.Command("sudo", "-n", "true").Run(); err == nil {
			return true
		}
	}
	return false
}

// IsChineseOS 判断是否为国产 OS
func (info *OSInfo) IsChineseOS() bool {
	switch info.Distribution {
	case DistUOS, DistDeepin, DistKylin, DistNeoKylin, DistOpenEuler, DistAnolis, DistOpenCloudOS:
		return true
	}
	return false
}

// String 输出可读字符串
func (info *OSInfo) String() string {
	return fmt.Sprintf("%s/%s kernel=%s pkg=%s",
		info.Distribution, info.Arch, info.Kernel, info.PackageMgr)
}
