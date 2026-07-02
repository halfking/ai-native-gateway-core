package envdetect

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// PrereqCheck 前置条件检查结果
type PrereqCheck struct {
	PortsOK        bool
	PortsInUse     []int
	PortDetails    map[int]string // 端口 -> 进程信息
	DiskFreeGB     int
	RAMMB          int
	NetworkOK      bool
	NetworkDetail  *NetworkStatus
}

// NetworkStatus 网络可达性
type NetworkStatus struct {
	InternalRegistryOK bool
	AliyunMirrorOK     bool
	DockerHubOK        bool
	PublicInternetOK   bool
}

// CheckPrereq 前置条件检查
func CheckPrereq(ports []int) (*PrereqCheck, error) {
	c := &PrereqCheck{
		PortDetails: make(map[int]string),
	}

	// 1. 端口检查
	for _, p := range ports {
		if isPortInUse(p) {
			c.PortsInUse = append(c.PortsInUse, p)
			c.PortDetails[p] = getPortProcess(p)
		}
	}
	c.PortsOK = len(c.PortsInUse) == 0

	// 2. 磁盘空间
	c.DiskFreeGB = getDiskFreeGB()

	// 3. 内存
	c.RAMMB = getRAMMB()

	// 4. 网络探测
	c.NetworkDetail = ProbeNetwork()
	c.NetworkOK = c.NetworkDetail.PublicInternetOK ||
		c.NetworkDetail.InternalRegistryOK ||
		c.NetworkDetail.AliyunMirrorOK

	return c, nil
}

// isPortInUse 检测端口是否被占用
// 用 Go 原生 net.Listen 试探，避免依赖 ss/netstat/lsof（跨平台可靠）
func isPortInUse(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true // 监听失败说明端口被占
	}
	_ = ln.Close()
	return false
}

// getPortProcess 获取占用端口的进程信息（跨平台）
func getPortProcess(port int) string {
	switch runtime.GOOS {
	case "linux":
		// Linux: ss -tlnp | grep :端口
		out, err := exec.Command("sh", "-c",
			fmt.Sprintf("ss -tlnp 2>/dev/null | grep ':%d ' | awk '{print $6}' | head -1", port)).Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
		// 兜底：lsof
		out, err = exec.Command("sh", "-c",
			fmt.Sprintf("lsof -i :%d -sTCP:LISTEN -t 2>/dev/null | xargs ps -p 2>/dev/null | tail -1", port)).Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
	case "darwin":
		// macOS: lsof -nP -iTCP:端口 -sTCP:LISTEN
		out, err := exec.Command("sh", "-c",
			fmt.Sprintf("lsof -nP -iTCP:%d -sTCP:LISTEN 2>/dev/null | tail -1", port)).Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
	case "windows":
		// Windows: netstat -ano | findstr :端口
		out, err := exec.Command("powershell", "-Command",
			fmt.Sprintf("Get-NetTCPConnection -LocalPort %d -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty OwningProcess | ForEach-Object { (Get-Process -Id $_ -ErrorAction SilentlyContinue).ProcessName }", port)).Output()
		if err == nil && len(out) > 0 {
			return strings.TrimSpace(string(out))
		}
	}
	return "未知进程"
}

// getDiskFreeGB 获取可用磁盘空间（GB）
func getDiskFreeGB() int {
	switch runtime.GOOS {
	case "windows":
		// wmic logicaldisk get freespace
		out, err := exec.Command("powershell", "-Command", "(Get-PSDrive -PSProvider FileSystem | Where-Object {$_.Used -ne $null} | Measure-Object -Property Free -Sum).Sum / 1GB").Output()
		if err != nil {
			return 0
		}
		v, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		return v
	case "darwin":
		// macOS: 用 -g（GB block size）
		out, err := exec.Command("df", "-g", ".").Output()
		if err != nil {
			return 0
		}
		lines := strings.Split(string(out), "\n")
		if len(lines) < 2 {
			return 0
		}
		fields := strings.Fields(lines[1])
		if len(fields) < 4 {
			return 0
		}
		// macOS df -g 输出如 "156 50 90 36% /"
		v, _ := strconv.Atoi(fields[3])
		return v
	default:
		// Linux: 用 -BG
		out, err := exec.Command("df", "-BG", ".").Output()
		if err != nil {
			return 0
		}
		lines := strings.Split(string(out), "\n")
		if len(lines) < 2 {
			return 0
		}
		fields := strings.Fields(lines[1])
		if len(fields) < 4 {
			return 0
		}
		// 第四列是可用空间（如 "156G"）
		v, _ := strconv.Atoi(strings.TrimSuffix(fields[3], "G"))
		return v
	}
}

// getRAMMB 获取内存大小（MB）
func getRAMMB() int {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		bytes, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		return int(bytes / 1024 / 1024)
	case "windows":
		out, err := exec.Command("powershell", "-Command",
			"(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory / 1MB").Output()
		if err != nil {
			return 0
		}
		v, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		return v
	default:
		out, err := exec.Command("sh", "-c", "grep MemTotal /proc/meminfo | awk '{print $2}'").Output()
		if err != nil {
			return 0
		}
		kb, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		return kb / 1024
	}
}

// ProbeNetwork 探测网络可达性
func ProbeNetwork() *NetworkStatus {
	s := &NetworkStatus{}
	timeout := 5 * time.Second

	s.InternalRegistryOK = probeRegistry("https://registry.kxpms.cn/v2/", timeout)
	s.AliyunMirrorOK = probeRegistry("https://registry.cn-hangzhou.aliyuncs.com/v2/", timeout)
	s.DockerHubOK = probeRegistry("https://registry-1.docker.io/v2/", timeout)
	s.PublicInternetOK = s.InternalRegistryOK || s.AliyunMirrorOK || s.DockerHubOK

	return s
}

// probeRegistry HEAD 探测 registry
func probeRegistry(url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}
