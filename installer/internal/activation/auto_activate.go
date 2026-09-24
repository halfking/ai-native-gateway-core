package activation

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/installer/internal/enrollment"
)

// ActivationStatus 激活结果状态
type ActivationStatus string

const (
	StatusActivated ActivationStatus = "activated"
	StatusTrial     ActivationStatus = "trial"
	StatusSkipped   ActivationStatus = "skipped"
	StatusFailed    ActivationStatus = "failed"
)

// AutoActivateOptions 描述 RunAutoActivate 的入参。
// 所有字段都从环境变量或 wizard 配置注入，函数本身不读取 os.Getenv()。
type AutoActivateOptions struct {
	InstallDir    string // 安装目录根路径；state/ 文件将写到 {InstallDir}/state/
	MasterURL     string // 主控端 URL（默认 https://llm.kxpms.cn）
	LicenseKey    string // 非空时走 ActivateOnline
	TrialEmail    string // 非空且 AgreeTerms 时走 RequestTrial
	AgreeTerms    bool
	StorageMode   string // 写进 activation.json 的 mode 字段；可为空
	InstallerVer  string // installer 版本；用于心跳与激活接口
	Skip          bool   // INSTALL_SKIP_ACTIVATION=1 时为 true；为 true 直接返回 nil
	RegisterMode  string // enrollment 实例类型，默认 "standalone"
	DiscoverIPFn  func(ctx context.Context) (string, error)
	OutboundProbe string // UDP 探测地址，默认 8.8.8.8:80
}

// ActivationState 写入 state/activation.json 的结构。
type ActivationState struct {
	InstanceID    string           `json:"instance_id"`
	InstanceToken string           `json:"instance_token"`
	DeviceCode    string           `json:"device_code"`
	IPAddress     string           `json:"ip_address"`
	Mode          string           `json:"mode"`
	Status        ActivationStatus `json:"status"`
	ActivatedAt   string           `json:"activated_at"`
	ExpiresAt     string           `json:"expires_at,omitempty"`
	Error         string           `json:"error,omitempty"`
	LicenseKey    string           `json:"license_key,omitempty"`
}

// discoverOutboundIP 用 UDP "拨号"探测本机出口 IP（不真正发包）。
// 5 秒超时。
func discoverOutboundIP(probeAddr string) (string, error) {
	if probeAddr == "" {
		probeAddr = "8.8.8.8:80"
	}
	conn, err := net.DialTimeout("udp", probeAddr, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("udp dial: %w", err)
	}
	defer conn.Close()

	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local == nil {
		return "", fmt.Errorf("no local addr")
	}
	return local.IP.String(), nil
}

// writeActivationState 写入 {installDir}/state/activation.json，权限 0600。
// 写入失败仅记录到 state.Error，不返回 error（不阻塞主流程）。
func writeActivationState(installDir string, st *ActivationState) error {
	if installDir == "" {
		return fmt.Errorf("installDir is empty")
	}
	stateDir := filepath.Join(installDir, "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("mkdir state: %w", err)
	}

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	target := filepath.Join(stateDir, "activation.json")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	_ = os.Chmod(target, 0600)
	return nil
}

// writeInstanceToken 写入 {installDir}/state/instance.token（仅 token 字符串）。
// 权限 0600。
func writeInstanceToken(installDir, token string) error {
	if installDir == "" {
		return fmt.Errorf("installDir is empty")
	}
	if token == "" {
		return fmt.Errorf("token is empty")
	}
	stateDir := filepath.Join(installDir, "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("mkdir state: %w", err)
	}

	target := filepath.Join(stateDir, "instance.token")
	if err := os.WriteFile(target, []byte(strings.TrimSpace(token)+"\n"), 0600); err != nil {
		return fmt.Errorf("write token: %w", err)
	}
	_ = os.Chmod(target, 0600)
	return nil
}

// RunAutoActivate 在 install 流程末尾执行：先调用 enrollment.Register，再根据 env 选择
// 在线激活或试用申请，最后把状态写到 {installDir}/state/activation.json 与
// {installDir}/state/instance.token。
//
// 行为契约：
//   - 不阻塞主流程：任何步骤失败仅写入 state.Error 并 return nil（installDir 不可写才返回 error）。
//   - opts.Skip=true：直接写 status=skipped 并返回 nil。
//   - opts.LicenseKey 与 opts.TrialEmail 都为空：写 status=skipped 并返回 nil。
//   - 网络/4xx 错误 → status=failed，error=...；不返回 error。
//   - 成功注册 → 同时写 instance.token 与 activation.json。
//   - 激活成功 → 覆盖 status=activated；试用成功 → status=trial。
func RunAutoActivate(ctx context.Context, opts AutoActivateOptions) error {
	if opts.InstallDir == "" {
		return fmt.Errorf("installDir is required")
	}

	st := &ActivationState{
		ActivatedAt: time.Now().UTC().Format(time.RFC3339),
		Mode:        opts.StorageMode,
	}

	if opts.Skip {
		st.Status = StatusSkipped
		if err := writeActivationState(opts.InstallDir, st); err != nil {
			// 安装目录不可写才返回 error，否则继续
			return err
		}
		return nil
	}

	if opts.MasterURL == "" {
		opts.MasterURL = "https://llm.kxpms.cn"
	}

	// 1) 收集本地标识
	instanceID, _ := GetOrCreateInstanceID()
	if instanceID != "" {
		st.InstanceID = instanceID
	}
	hardwareHash, _ := GetHardwareHash()
	deviceName := GetDeviceName()

	// 2) 出口 IP
	discover := opts.DiscoverIPFn
	if discover == nil {
		discover = func(ctx context.Context) (string, error) {
			return discoverOutboundIP(opts.OutboundProbe)
		}
	}
	ip, ipErr := discover(ctx)
	if ipErr == nil {
		st.IPAddress = ip
	}

	// 3) enrollment.Register
	enrollClient := enrollment.NewClient(opts.MasterURL)
	registerReq := enrollment.RegisterRequest{
		InstanceID:     instanceID,
		InstanceType:   opts.RegisterMode,
		Hostname:       deviceName,
		IPAddress:      ip,
		Version:        opts.InstallerVer,
		HardwareHash:   hardwareHash,
		LicenseKeyHash: hardwareHash,
		PublicKey:      "placeholder-ed25519-base64",
	}
	if registerReq.InstanceType == "" {
		registerReq.InstanceType = "standalone"
	}

	regResp, regErr := enrollClient.Register(ctx, registerReq)
	if regErr != nil {
		st.Status = StatusFailed
		st.Error = fmt.Sprintf("register: %v", regErr)
		_ = writeActivationState(opts.InstallDir, st)
		return nil
	}

	// 4) 写 instance.token + activation.json（含 token）
	if regResp != nil && regResp.InstanceToken != "" {
		st.InstanceToken = regResp.InstanceToken
		st.DeviceCode = regResp.InstanceToken
		if err := writeInstanceToken(opts.InstallDir, regResp.InstanceToken); err != nil {
			st.Error = fmt.Sprintf("write instance token: %v", err)
		}
	}

	// 5) 选择激活分支
	switch {
	case opts.LicenseKey != "":
		actClient := NewClient(opts.MasterURL)
		actResp, actErr := actClient.ActivateOnline(opts.LicenseKey, hardwareHash, instanceID, deviceName, opts.InstallerVer)
		if actErr != nil {
			st.Status = StatusFailed
			st.Error = fmt.Sprintf("activate: %v", actErr)
			_ = writeActivationState(opts.InstallDir, st)
			return nil
		}
		st.Status = StatusActivated
		st.ExpiresAt = actResp.ExpiresAt
		st.LicenseKey = opts.LicenseKey

	case opts.TrialEmail != "" && opts.AgreeTerms:
		actClient := NewClient(opts.MasterURL)
		trialResp, trialErr := actClient.RequestTrial(opts.TrialEmail, opts.AgreeTerms)
		if trialErr != nil {
			st.Status = StatusFailed
			st.Error = fmt.Sprintf("trial: %v", trialErr)
			_ = writeActivationState(opts.InstallDir, st)
			return nil
		}
		st.Status = StatusTrial
		st.ExpiresAt = trialResp.ExpiresAt
		if trialResp.LicenseKey != "" {
			st.LicenseKey = trialResp.LicenseKey
		}

	default:
		st.Status = StatusSkipped
		st.Error = "no license key and no trial email; activation skipped"
	}

	if err := writeActivationState(opts.InstallDir, st); err != nil {
		// 安装目录不可写：返回 error 让上层 logWarn
		return err
	}
	return nil
}
