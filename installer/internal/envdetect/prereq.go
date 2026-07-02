package envdetect

import (
	"context"
	"fmt"
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
	c := &PrereqCheck{}

	// 1. 端口检查
	for _, p := range ports {
		if isPortInUse(p) {
			c.PortsInUse = append(c.PortsInUse, p)
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
func isPortInUse(port int) bool {
	switch runtime.GOOS {
	case "windows":
		out, err := exec.Command("netstat", "-ano").Output()
		if err != nil {
			return false
		}
		return strings.Contains(string(out), fmt.Sprintf(":%d ", port))
	default:
		// Linux/macOS: 用 ss 或 lsof 或 netstat
		out, err := exec.Command("sh", "-c", fmt.Sprintf("ss -tln 2>/dev/null | grep -q ':%d ' || netstat -tln 2>/dev/null | grep -q ':%d ' || lsof -iTCP:%d -sTCP:LISTEN 2>/dev/null", port, port, port)).Output()
		if err != nil {
			return false
		}
		return strings.TrimSpace(string(out)) != ""
	}
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
	default:
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
