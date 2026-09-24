package activation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	LicenseKey    string // 非空时随 register 提交（主控同调用完成设备激活）
	TrialEmail    string // 非空且 AgreeTerms 时先 RequestTrial 换 license key 再 register
	AgreeTerms    bool
	StorageMode   string // 写进 activation.json 的 mode 字段；可为空
	InstallerVer  string // installer 版本；用于 register 接口
	Skip          bool   // INSTALL_SKIP_ACTIVATION=1 时为 true；为 true 直接返回 nil
	RegisterMode  string // enrollment 实例类型，默认 "standalone"
	DiscoverIPFn  func(ctx context.Context) (string, error)
	OutboundProbe string // UDP 探测地址，默认 8.8.8.8:80
}

// ActivationState 写入 state/activation.json 的结构。
// 注意：instance_token（主控签发的 JWT 心跳凭据）绝不写入本文件 —— 它只落
// state/instance.token（0600），避免经 launcher /status 接口外泄。
type ActivationState struct {
	InstanceID  string           `json:"instance_id"`
	DeviceCode  string           `json:"device_code"`
	IPAddress   string           `json:"ip_address"`
	Mode        string           `json:"mode"`
	Status      ActivationStatus `json:"status"`
	ActivatedAt string           `json:"activated_at"`
	ExpiresAt   string           `json:"expires_at,omitempty"`
	Error       string           `json:"error,omitempty"`
	LicenseKey  string           `json:"license_key,omitempty"`
}

// DeriveDeviceCode 从稳定的 instance_id 派生可展示的设备码。
// 主控端注册响应不含独立 device_code 字段，因此由本地派生：
// "GW-" + sha256(instance_id) 前 12 个 hex 字符。非凭据、可安全展示。
func DeriveDeviceCode(instanceID string) string {
	sum := sha256.Sum256([]byte(instanceID))
	return "GW-" + hex.EncodeToString(sum[:])[:12]
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
	if err := os.MkdirAll(stateDir, 0700); err != nil {
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
// 权限 0600。这是 instance_token JWT 唯一的落盘位置。
func writeInstanceToken(installDir, token string) error {
	if installDir == "" {
		return fmt.Errorf("installDir is empty")
	}
	if token == "" {
		return fmt.Errorf("token is empty")
	}
	stateDir := filepath.Join(installDir, "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return fmt.Errorf("mkdir state: %w", err)
	}

	target := filepath.Join(stateDir, "instance.token")
	if err := os.WriteFile(target, []byte(strings.TrimSpace(token)+"\n"), 0600); err != nil {
		return fmt.Errorf("write token: %w", err)
	}
	_ = os.Chmod(target, 0600)
	return nil
}

// ensureKeypairAt 在 keyPath 所在目录生成/复用 ed25519 密钥对：
// 私钥 seed hex 写入 keyPath（0600），返回 base64(std) 公钥。
// 已有私钥文件时直接复用，保证同一安装重复 register 公钥稳定。
func ensureKeypairAt(keyPath string) (string, error) {
	if data, err := os.ReadFile(keyPath); err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err == nil && len(seed) == ed25519.SeedSize {
			priv := ed25519.NewKeyFromSeed(seed)
			return base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)), nil
		}
		// 文件损坏则重新生成（覆盖写入）
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ed25519 key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return "", fmt.Errorf("mkdir key dir: %w", err)
	}
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write key: %w", err)
	}
	_ = os.Chmod(keyPath, 0600)
	return base64.StdEncoding.EncodeToString(pub), nil
}

// EnsureInstallKeypair 返回随 register 提交的公钥，私钥落
// {installDir}/state/instance.ed25519（0600）。
func EnsureInstallKeypair(installDir string) (string, error) {
	return ensureKeypairAt(filepath.Join(installDir, "state", "instance.ed25519"))
}

// EnsureHomeKeypair 供 activate 子命令（无 installDir 上下文）使用，
// 私钥落 ~/.kx-gateway/instance.ed25519，与 GetOrCreateInstanceID 同目录。
func EnsureHomeKeypair() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return ensureKeypairAt(filepath.Join(homeDir, ".kx-gateway", "instance.ed25519"))
}

// RunAutoActivate 在 install 流程末尾执行：向主控端注册实例并完成激活，
// 状态写到 {installDir}/state/activation.json，凭据写到
// {installDir}/state/instance.token。
//
// 主控端契约（cmd/license-authority/register_handler.go，本仓源码）：
//   - POST /api/v1/instances/register 的 license_key_hash 字段，对新设备
//     会被当作完整 license key 查库（旧版安装器兼容路径），查得即在同一
//     调用里 ActivateDevice —— 因此 register 即激活，无需第二个接口
//     （主控不存在 /api/v1/license/activate 端点）。
//   - 试用：POST /api/v1/license/trial 换取 license key 后同样走 register。
//
// 行为契约：
//   - 不阻塞主流程：任何步骤失败仅写入 state.Error 并 return nil（installDir 不可写才返回 error）。
//   - opts.Skip=true：直接写 status=skipped 并返回 nil。
//   - LicenseKey 与 TrialEmail 都为空：写 status=skipped，不发起网络调用
//     （避免对全新设备产生必然 404 的注册噪音；同机重装场景由下次携带
//     license key 的安装完成注册）。
//   - 网络/4xx 错误 → status=failed，error=...；不返回 error。
//   - 注册成功 → 写 instance.token；license key 来自试用 → status=trial，
//     否则 status=activated。
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

	hasTrial := opts.TrialEmail != "" && opts.AgreeTerms
	if opts.LicenseKey == "" && !hasTrial {
		st.Status = StatusSkipped
		st.Error = "no license key and no trial email; activation skipped"
		return writeActivationState(opts.InstallDir, st)
	}

	if opts.MasterURL == "" {
		opts.MasterURL = "https://llm.kxpms.cn"
	}

	// 1) 收集本地标识
	instanceID, _ := GetOrCreateInstanceID()
	if instanceID != "" {
		st.InstanceID = instanceID
	}
	st.DeviceCode = DeriveDeviceCode(instanceID)
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

	// 3) ed25519 keypair（公钥随 register 提交，私钥留在本地 state/）
	publicKey, keyErr := EnsureInstallKeypair(opts.InstallDir)
	if keyErr != nil {
		st.Status = StatusFailed
		st.Error = keyErr.Error()
		if wErr := writeActivationState(opts.InstallDir, st); wErr != nil {
			return wErr
		}
		return nil
	}

	// 4) 无 license key 时先走试用申请换取 key
	licenseKey := opts.LicenseKey
	isTrial := false
	if licenseKey == "" {
		actClient := NewClient(opts.MasterURL)
		trialResp, trialErr := actClient.RequestTrial(opts.TrialEmail, opts.AgreeTerms)
		if trialErr != nil {
			st.Status = StatusFailed
			st.Error = fmt.Sprintf("trial: %v", trialErr)
			_ = writeActivationState(opts.InstallDir, st)
			return nil
		}
		licenseKey = trialResp.LicenseKey
		st.ExpiresAt = trialResp.ExpiresAt
		if trialResp.LicenseKey != "" {
			st.LicenseKey = trialResp.LicenseKey
		}
		isTrial = true
	}

	// 5) register：license_key_hash 携带完整 license key（主控新设备兼容路径，
	//    查得即激活）；hardware_hash 供同机重装走已注册设备分支。
	registerMode := opts.RegisterMode
	if registerMode == "" {
		registerMode = "standalone"
	}
	enrollClient := enrollment.NewClient(opts.MasterURL)
	regResp, regErr := enrollClient.Register(ctx, enrollment.RegisterRequest{
		InstanceID:     instanceID,
		InstanceType:   registerMode,
		Hostname:       deviceName,
		IPAddress:      ip,
		Version:        opts.InstallerVer,
		HardwareHash:   hardwareHash,
		LicenseKeyHash: licenseKey,
		PublicKey:      publicKey,
	})
	if regErr != nil {
		st.Status = StatusFailed
		st.Error = fmt.Sprintf("register: %v", regErr)
		_ = writeActivationState(opts.InstallDir, st)
		return nil
	}

	// 6) instance.token 只落专用文件（0600），不进 activation.json
	if regResp != nil && regResp.InstanceToken != "" {
		if err := writeInstanceToken(opts.InstallDir, regResp.InstanceToken); err != nil {
			// 记录错误但保留激活成功状态 —— 运维需要看到 token 写入失败
			st.Error = fmt.Sprintf("write instance token: %v", err)
		}
	}

	if isTrial {
		st.Status = StatusTrial
	} else {
		st.Status = StatusActivated
	}

	if err := writeActivationState(opts.InstallDir, st); err != nil {
		// 安装目录不可写：返回 error 让上层 logWarn
		return err
	}
	return nil
}
