# 运维平台与License管理系统 - 详细架构设计 V2

> 基于对现有代码库的深度分析，确保新模块与现有架构模式完全一致

---

## 零、设计约束与前提

### 现有架构模式（必须遵循）

| 模式 | 说明 | 参考文件 |
|------|------|---------|
| DB Schema在启动时应用 | `db.Open()` 调用 `ensure*()` 方法 | `db/db.go` |
| 后台Worker | `bg.NewXxxWorker().Start(ctx)` | `bg/*.go` |
| Admin Handler | `h.admin(fn)` / `h.superAdmin(fn)` 包装器 | `admin/handler.go` |
| 依赖注入 | `Set*()` 方法，避免循环导入 | `admin/handler.go` |
| RLS双策略 | `tenant_isolation` + `super_admin_bypass` | 所有租户表 |
| Event Bus | `eventbus.MemoryBus` 进程内pub/sub | `eventbus/memory_bus.go` |
| Settings | `Spec` + `Registry` + `Backend` | `settings/specs.go` |
| 配置热更新 | `config.Store` (atomic.Pointer) | `config/config.go` |

### 迁移编号约定

- **startup目录**: 最新 = 366 → 新表使用 **367+**
- **domain目录**: 最新 = 334 → 新表使用 **335+**

---

## 一、License管理模块

### 1.1 目录结构

```
license/                          # 新增顶层包
├── types.go                      # 核心类型定义
├── fingerprint.go                # 硬件指纹生成
├── crypto.go                     # 加密/签名/验证
├── validator.go                  # License验证器
├── activator.go                  # 在线激活客户端
├── offline.go                    # 离线激活
├── device_manager.go             # 设备管理（2设备限制）
├── store.go                      # 数据库存储接口
├── store_pgx.go                  # PostgreSQL实现
├── admin_api.go                  # Admin API Handler
└── center_api.go                 # 中心节点通信
```

### 1.2 核心类型 (`license/types.go`)

```go
package license

import "time"

// License 完整的License信息
type License struct {
    ID          int64     `json:"id"`
    LicenseKey  string    `json:"license_key"`
    CustomerName  string  `json:"customer_name"`
    CustomerEmail string  `json:"customer_email"`
    MaxDevices  int       `json:"max_devices"`      // 默认2
    Features    []string  `json:"features"`
    ExpiresAt   time.Time `json:"expires_at"`
    CreatedAt   time.Time `json:"created_at"`
    RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

// SignedLicense 签名后的License
type SignedLicense struct {
    Data      []byte `json:"data"`      // License JSON序列化
    Signature []byte `json:"signature"` // RSA签名
}

// Device 激活设备记录
type Device struct {
    ID              int64      `json:"id"`
    LicenseID       int64      `json:"license_id"`
    InstanceID      string     `json:"instance_id"`
    HardwareHash    string     `json:"hardware_hash"`
    DeviceName      string     `json:"device_name"`
    ActivatedAt     time.Time  `json:"activated_at"`
    LastHeartbeat   *time.Time `json:"last_heartbeat,omitempty"`
    Status          string     `json:"status"` // active, deactivated, suspended
    DeactivatedAt   *time.Time `json:"deactivated_at,omitempty"`
    DeactivateReason string   `json:"deactivate_reason,omitempty"`
}

// ActivationRequest 在线激活请求
type ActivationRequest struct {
    LicenseKey   string `json:"license_key"`
    HardwareHash string `json:"hardware_hash"`
    InstanceID   string `json:"instance_id"`
    DeviceName   string `json:"device_name"`
    Version      string `json:"version"`
}

// ActivationResponse 在线激活响应
type ActivationResponse struct {
    Success       bool           `json:"success"`
    SignedLicense *SignedLicense `json:"signed_license,omitempty"`
    ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
    ActiveDevices []Device       `json:"active_devices"` // 当前已激活设备
    Message       string         `json:"message,omitempty"`
    NeedDeactivate bool          `json:"need_deactivate,omitempty"` // 设备数已满
}

// DeactivateRequest 设备停用请求
type DeactivateRequest struct {
    LicenseKey   string `json:"license_key"`
    HardwareHash string `json:"hardware_hash"` // 要停用的设备
    Reason       string `json:"reason"`
}

// OfflineRequest 离线激活请求
type OfflineRequest struct {
    LicenseKey   string    `json:"license_key"`
    HardwareHash string    `json:"hardware_hash"`
    InstanceID   string    `json:"instance_id"`
    DeviceName   string    `json:"device_name"`
    RequestID    string    `json:"request_id"`
    Timestamp    time.Time `json:"timestamp"`
}
```

### 1.3 硬件指纹 (`license/fingerprint.go`)

```go
package license

import (
    "crypto/sha256"
    "fmt"
    "net"
    "os"
    "runtime"
    "strings"

    "github.com/denisbrodbeck/machineid"
    "github.com/shirou/gopsutil/v3/host"
    "github.com/shirou/gopsutil/v3/cpu"
)

// Fingerprint 硬件指纹
type Fingerprint struct {
    MachineID    string   `json:"machine_id"`
    CPUInfo      string   `json:"cpu_info"`
    HostID       string   `json:"host_id"`
    OS           string   `json:"os"`
    Arch         string   `json:"arch"`
    PrimaryMAC   string   `json:"primary_mac"` // 主网卡MAC
}

// GenerateFingerprint 生成当前机器的硬件指纹
// 返回稳定、可复现的指纹用于License绑定
func GenerateFingerprint() (*Fingerprint, error) {
    fp := &Fingerprint{
        OS:   runtime.GOOS,
        Arch: runtime.GOARCH,
    }

    // 1. MachineID - 跨重启持久化的唯一机器ID
    mid, err := machineid.ID()
    if err == nil {
        fp.MachineID = mid
    }

    // 2. CPU信息
    if cpuInfo, err := cpu.Info(); err == nil && len(cpuInfo) > 0 {
        fp.CPUInfo = cpuInfo[0].ModelName
    }

    // 3. 主机ID
    if h, err := host.Info(); err == nil {
        fp.HostID = h.HostID
    }

    // 4. 主网卡MAC地址
    fp.PrimaryMAC = getPrimaryMAC()

    return fp, nil
}

// Hash 生成设备指纹哈希（前16字节hex）
func (fp *Fingerprint) Hash() string {
    raw := fmt.Sprintf("%s|%s|%s|%s",
        fp.MachineID, fp.CPUInfo, fp.HostID, fp.PrimaryMAC)
    hash := sha256.Sum256([]byte(raw))
    return fmt.Sprintf("%x", hash[:16])
}

// getPrimaryMAC 获取主网卡MAC地址（排除loopback和虚拟网卡）
func getPrimaryMAC() string {
    interfaces, err := net.Interfaces()
    if err != nil {
        return ""
    }
    for _, iface := range interfaces {
        if iface.Flags&net.FlagLoopback != 0 {
            continue
        }
        if iface.Flags&net.FlagUp == 0 {
            continue
        }
        if len(iface.HardwareAddr) == 0 {
            continue
        }
        // 排除常见的虚拟网卡前缀
        mac := iface.HardwareAddr.String()
        if strings.HasPrefix(mac, "00:00:00:00:00:00") {
            continue
        }
        return mac
    }
    return ""
}

// Match 模糊匹配：允许单个硬件组件变更（网卡更换等）
// 返回匹配分数 0.0 - 1.0
func (fp *Fingerprint) MatchScore(stored *Fingerprint) float64 {
    if stored == nil {
        return 0
    }
    score := 0.0
    total := 0.0

    // MachineID: 核心标识，权重最高
    total += 3.0
    if fp.MachineID == stored.MachineID {
        score += 3.0
    }

    // CPU: 一般不会变
    total += 2.0
    if fp.CPUInfo == stored.CPUInfo {
        score += 2.0
    }

    // HostID: 操作系统相关
    total += 1.0
    if fp.HostID == stored.HostID {
        score += 1.0
    }

    // MAC地址: 可能更换网卡
    total += 1.0
    if fp.PrimaryMAC == stored.PrimaryMAC {
        score += 1.0
    }

    return score / total
}

// MatchThreshold 匹配阈值（允许更换1个组件）
const MatchThreshold = 0.6
```

### 1.4 加密与签名 (`license/crypto.go`)

```go
package license

import (
    "crypto"
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "crypto/rsa"
    "crypto/sha256"
    "crypto/x509"
    "encoding/base64"
    "encoding/json"
    "encoding/pem"
    "errors"
    "io"
    "time"

    "github.com/golang-jwt/jwt/v5"
)

// CryptoConfig 加密配置
type CryptoConfig struct {
    // 签名密钥对（RSA-2048）
    PrivateKey *rsa.PrivateKey
    PublicKey  *rsa.PublicKey

    // AES加密密钥（用于离线文件加密）
    AESKey []byte // 32 bytes for AES-256

    // JWT签名密钥
    JWTSecret []byte

    // License有效期默认值
    DefaultExpiry time.Duration
}

// SignLicense 签名License数据
func (c *CryptoConfig) SignLicense(lic *License) (*SignedLicense, error) {
    data, err := json.Marshal(lic)
    if err != nil {
        return nil, err
    }

    hash := sha256.Sum256(data)
    signature, err := rsa.SignPKCS1v15(rand.Reader, c.PrivateKey, crypto.SHA256, hash[:])
    if err != nil {
        return nil, err
    }

    return &SignedLicense{
        Data:      data,
        Signature: signature,
    }, nil
}

// VerifyLicense 验证签名并解析License
func (c *CryptoConfig) VerifyLicense(signed *SignedLicense) (*License, error) {
    if signed == nil {
        return nil, errors.New("nil signed license")
    }

    // 1. 验证签名
    hash := sha256.Sum256(signed.Data)
    if err := rsa.VerifyPKCS1v15(c.PublicKey, crypto.SHA256, hash[:], signed.Signature); err != nil {
        return nil, errors.New("license signature invalid")
    }

    // 2. 解析License
    var lic License
    if err := json.Unmarshal(signed.Data, &lic); err != nil {
        return nil, err
    }

    return &lic, nil
}

// EncryptAES AES-256-GCM加密（用于离线文件）
func (c *CryptoConfig) EncryptAES(data []byte) ([]byte, error) {
    block, err := aes.NewCipher(c.AESKey)
    if err != nil {
        return nil, err
    }
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }
    nonce := make([]byte, gcm.NonceSize())
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
        return nil, err
    }
    return gcm.Seal(nonce, nonce, data, nil), nil
}

// DecryptAES AES-256-GCM解密
func (c *CryptoConfig) DecryptAES(encrypted []byte) ([]byte, error) {
    block, err := aes.NewCipher(c.AESKey)
    if err != nil {
        return nil, err
    }
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }
    nonceSize := gcm.NonceSize()
    if len(encrypted) < nonceSize {
        return nil, errors.New("ciphertext too short")
    }
    nonce, ciphertext := encrypted[:nonceSize], encrypted[nonceSize:]
    return gcm.Open(nil, nonce, ciphertext, nil)
}

// GenerateJWT 生成激活Token（用于后续心跳验证）
func (c *CryptoConfig) GenerateJWT(instanceID string, licenseKey string, expiresAt time.Time) (string, error) {
    claims := jwt.MapClaims{
        "instance_id": instanceID,
        "license_key": licenseKey,
        "exp":         expiresAt.Unix(),
        "iat":         time.Now().Unix(),
    }
    token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    return token.SignedString(c.JWTSecret)
}

// VerifyJWT 验证JWT Token
func (c *CryptoConfig) VerifyJWT(tokenStr string) (jwt.MapClaims, error) {
    token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
        if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, errors.New("unexpected signing method")
        }
        return c.JWTSecret, nil
    })
    if err != nil {
        return nil, err
    }
    claims, ok := token.Claims.(jwt.MapClaims)
    if !ok || !token.Valid {
        return nil, errors.New("invalid token")
    }
    return claims, nil
}

// LoadPublicKeyFromPEM 从PEM加载RSA公钥
func LoadPublicKeyFromPEM(pemStr string) (*rsa.PublicKey, error) {
    block, _ := pem.Decode([]byte(pemStr))
    if block == nil {
        return nil, errors.New("invalid PEM")
    }
    pub, err := x509.ParsePKIXPublicKey(block.Bytes)
    if err != nil {
        return nil, err
    }
    rsaPub, ok := pub.(*rsa.PublicKey)
    if !ok {
        return nil, errors.New("not RSA public key")
    }
    return rsaPub, nil
}

// MarshalToBase64 序列化SignedLicense为base64字符串
func MarshalToBase64(signed *SignedLicense) (string, error) {
    data, err := json.Marshal(signed)
    if err != nil {
        return "", err
    }
    return base64.StdEncoding.EncodeToString(data), nil
}

// UnmarshalFromBase64 从base64字符串反序列化SignedLicense
func UnmarshalFromBase64(b64 string) (*SignedLicense, error) {
    data, err := base64.StdEncoding.DecodeString(b64)
    if err != nil {
        return nil, err
    }
    var signed SignedLicense
    if err := json.Unmarshal(data, &signed); err != nil {
        return nil, err
    }
    return &signed, nil
}
```

### 1.5 存储接口 (`license/store.go`)

```go
package license

import (
    "context"
    "time"
)

// Store License存储接口（遵循现有项目接口定义模式）
type Store interface {
    // License CRUD
    GetLicense(ctx context.Context, licenseKey string) (*License, error)
    CreateLicense(ctx context.Context, lic *License) error
    RevokeLicense(ctx context.Context, licenseKey string) error

    // 设备管理
    GetActiveDevices(ctx context.Context, licenseKey string) ([]Device, error)
    GetDeviceByHardwareHash(ctx context.Context, licenseKey, hardwareHash string) (*Device, error)
    ActivateDevice(ctx context.Context, dev *Device) error
    DeactivateDevice(ctx context.Context, licenseKey, hardwareHash, reason string) error
    UpdateHeartbeat(ctx context.Context, licenseKey, hardwareHash string) error

    // 离线激活
    CreateOfflineRequest(ctx context.Context, req *OfflineRequest) error
    GetOfflineRequest(ctx context.Context, requestID string) (*OfflineRequest, error)
    ApproveOfflineRequest(ctx context.Context, requestID string, signedLicense *SignedLicense) error

    // 统计
    CountActiveDevices(ctx context.Context, licenseKey string) (int, error)
    ListAllLicenses(ctx context.Context, offset, limit int) ([]License, int, error)
    ListAllDevices(ctx context.Context, licenseKey string) ([]Device, error)
}
```

### 1.6 数据库Schema (迁移文件 `startup/367_license_management.sql`)

```sql
-- 367_license_management.sql
-- License管理模块核心表

-- License定义表
CREATE TABLE IF NOT EXISTS licenses (
    id              BIGSERIAL PRIMARY KEY,
    license_key     TEXT NOT NULL UNIQUE,
    customer_name   TEXT NOT NULL,
    customer_email  TEXT NOT NULL,
    max_devices     INT NOT NULL DEFAULT 2,
    features        JSONB NOT NULL DEFAULT '[]'::jsonb,
    signed_license  TEXT NOT NULL,  -- base64编码的SignedLicense
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at      TIMESTAMPTZ,
    notes           TEXT
);
CREATE INDEX IF NOT EXISTS idx_licenses_key ON licenses (license_key);
CREATE INDEX IF NOT EXISTS idx_licenses_expires ON licenses (expires_at) WHERE expires_at IS NOT NULL;

-- 设备激活记录表
CREATE TABLE IF NOT EXISTS license_devices (
    id                BIGSERIAL PRIMARY KEY,
    license_id        BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    instance_id       TEXT NOT NULL,
    hardware_hash     TEXT NOT NULL,
    device_name       TEXT NOT NULL DEFAULT '',
    activated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat_at TIMESTAMPTZ,
    status            TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'deactivated', 'suspended')),
    deactivated_at    TIMESTAMPTZ,
    deactivate_reason TEXT,
    metadata          JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (license_id, hardware_hash)
);
CREATE INDEX IF NOT EXISTS idx_ld_license_status ON license_devices (license_id, status);
CREATE INDEX IF NOT EXISTS idx_ld_instance ON license_devices (instance_id);
CREATE INDEX IF NOT EXISTS idx_ld_heartbeat ON license_devices (last_heartbeat_at DESC);

-- 离线激活请求表
CREATE TABLE IF NOT EXISTS offline_activation_requests (
    id              BIGSERIAL PRIMARY KEY,
    license_id      BIGINT NOT NULL REFERENCES licenses(id),
    request_id      TEXT NOT NULL UNIQUE,
    hardware_hash   TEXT NOT NULL,
    instance_id     TEXT NOT NULL,
    device_name     TEXT NOT NULL DEFAULT '',
    request_data    JSONB NOT NULL, -- 离线请求的完整数据
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected')),
    signed_license  TEXT,           -- 审批后的签名License
    reviewed_by     TEXT,
    reviewed_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_oar_status ON offline_activation_requests (status);

-- 激活审计日志
CREATE TABLE IF NOT EXISTS license_activation_log (
    id              BIGSERIAL PRIMARY KEY,
    license_key     TEXT NOT NULL,
    action          TEXT NOT NULL,  -- activate, deactivate, heartbeat, revoke
    instance_id     TEXT,
    hardware_hash   TEXT,
    ip_address      TEXT,
    user_agent      TEXT,
    details         JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_lal_key_ts ON license_activation_log (license_key, created_at DESC);

-- RLS: license管理是全局系统级功能，不按租户隔离
-- 超级管理员通过 bypass_rls 访问
```

### 1.7 Admin API (`license/admin_api.go`)

```
API端点设计:

# License管理（超级管理员）
POST   /api/admin/licenses                  创建License
GET    /api/admin/licenses                  列出所有License
GET    /api/admin/licenses/:key             获取License详情
DELETE /api/admin/licenses/:key             撤销License

# 设备管理
GET    /api/admin/licenses/:key/devices     列出设备
POST   /api/admin/licenses/:key/deactivate  停用指定设备

# 离线激活管理
GET    /api/admin/offline-requests          列出待审批请求
POST   /api/admin/offline-requests/:id/approve  审批通过
POST   /api/admin/offline-requests/:id/reject   审批拒绝

# 网关实例端（实例自身调用）
POST   /api/license/activate                在线激活
POST   /api/license/deactivate              停用设备
POST   /api/license/heartbeat               心跳保活
POST   /api/license/offline-request         生成离线请求
POST   /api/license/import-response         导入离线响应
GET    /api/license/status                  当前License状态
```

### 1.8 在线激活流程

```
┌─────────────┐                          ┌──────────────┐
│  Gateway    │                          │  Admin Server│
│  实例       │                          │  (License服务)│
└──────┬──────┘                          └──────┬───────┘
       │                                        │
       │  1. POST /api/license/activate         │
       │  {license_key, hwid, instance_id}     │
       │ ─────────────────────────────────────▶│
       │                                        │ 2. 验证License Key
       │                                        │ 3. 查询已激活设备
       │                                        │
       │                                        ├─ 设备数 < max_devices?
       │                                        │  YES → 4a. 直接激活
       │                                        │  NO  → 4b. 返回NeedDeactivate=true
       │                                        │         附带ActiveDevices列表
       │  4b. {success:false,                   │
       │       need_deactivate:true,            │
       │       active_devices:[...]}            │
       │◀───────────────────────────────────── │
       │                                        │
       │  5. 用户选择要替换的设备               │
       │  6. POST /api/license/deactivate       │
       │  {hardware_hash:"xxx"}                │
       │ ─────────────────────────────────────▶│
       │                                        │ 7. 标记旧设备deactivated
       │  8. 返回成功                           │
       │◀───────────────────────────────────── │
       │                                        │
       │  9. POST /api/license/activate (重试)  │
       │ ─────────────────────────────────────▶│
       │                                        │ 10. 激活新设备
       │                                        │ 11. 签名License
       │  12. {success:true, signed_license}   │
       │◀───────────────────────────────────── │
       │                                        │
       │  13. 本地缓存SignedLicense            │
       │  14. 启动心跳定时器                    │
       │                                        │
```

### 1.9 防破解策略

| 层级 | 措施 | 说明 |
|------|------|------|
| L1 | RSA-2048签名 | License数据不可篡改 |
| L2 | AES-256-GCM加密 | 离线文件传输加密 |
| L3 | 硬件指纹绑定 | 2设备限制+模糊匹配 |
| L4 | JWT心跳 | 定期向中心验证有效性 |
| L5 | 时间篡改检测 | NTP校验+单调时钟 |
| L6 | 二进制混淆 | garble混淆Go代码 |
| L7 | 代码段校验 | 运行时验证自身完整性 |

---

## 二、故障自愈模块

### 2.1 目录结构

```
fault/                            # 新增顶层包
├── types.go                      # 故障事件类型定义
├── detector.go                   # 故障检测器接口
├── analyzer.go                   # 故障分析与摘要生成
├── strategy.go                   # 修复策略引擎
├── strategies/
│   ├── restart.go                # 重启策略
│   ├── failover.go               # 切换策略
│   └── rollback.go               # 回滚策略
├── store.go                      # 存储接口
├── store_pgx.go                  # PostgreSQL实现
├── reporter.go                   # 向中心节点上报
├── admin_api.go                  # Admin API Handler
└── worker.go                     # 后台Worker（检测+修复循环）
```

### 2.2 核心类型 (`fault/types.go`)

```go
package fault

import "time"

// FaultType 故障类型枚举
type FaultType string

const (
    FaultRouteError        FaultType = "route_error"         // 无可用候选节点
    FaultCredentialFailure FaultType = "credential_failure"  // 凭据认证/配额失败
    FaultHighFailureRate   FaultType = "high_failure_rate"   // 失败率过高
    FaultHighLatency       FaultType = "high_latency"        // P99延迟超阈值
    FaultCircuitOpen       FaultType = "circuit_open"        // 熔断器打开
    FaultPoolExhausted     FaultType = "pool_exhausted"      // 连接池耗尽
)

// Severity 严重程度
type Severity string

const (
    SeverityCritical Severity = "critical"
    SeverityMajor    Severity = "major"
    SeverityMinor    Severity = "minor"
    SeverityInfo     Severity = "info"
)

// FaultStatus 故障处理状态
type FaultStatus string

const (
    FaultStatusDetected   FaultStatus = "detected"
    FaultStatusAnalyzing  FaultStatus = "analyzing"
    FaultStatusFixing     FaultStatus = "fixing"
    FaultStatusResolved   FaultStatus = "resolved"
    FaultStatusEscalated  FaultStatus = "escalated"
)

// FixStrategy 修复策略类型
type FixStrategy string

const (
    FixRestart    FixStrategy = "restart"     // 重启组件
    FixFailover   FixStrategy = "failover"    // 切换备用
    FixRollback   FixStrategy = "rollback"    // 回滚配置
    FixNone       FixStrategy = "none"        // 仅告警
)

// FaultEvent 故障事件
type FaultEvent struct {
    ID          int64       `json:"id"`
    InstanceID  string      `json:"instance_id"`
    FaultType   FaultType   `json:"fault_type"`
    Severity    Severity    `json:"severity"`
    Status      FaultStatus `json:"status"`
    DetectedAt  time.Time   `json:"detected_at"`
    ResolvedAt  *time.Time  `json:"resolved_at,omitempty"`

    // 关联信息
    CredentialID *int64     `json:"credential_id,omitempty"`
    ModelName    string     `json:"model_name,omitempty"`
    ProviderName string     `json:"provider_name,omitempty"`

    // 诊断信息
    RootCause    string     `json:"root_cause,omitempty"`
    ErrorDetails string     `json:"error_details,omitempty"`
    RawLogs      string     `json:"raw_logs,omitempty"`
    Summary      string     `json:"summary,omitempty"` // AI摘要

    // 修复信息
    FixStrategy  *FixStrategy `json:"fix_strategy,omitempty"`
    FixApplied   string      `json:"fix_applied,omitempty"`
    FixResult    string      `json:"fix_result,omitempty"`
    FixDurationMs *int       `json:"fix_duration_ms,omitempty"`

    Metadata     map[string]any `json:"metadata,omitempty"`
}

// FaultSnapshot 故障快照（用于摘要分析）
type FaultSnapshot struct {
    Events       []FaultEvent    `json:"events"`
    TimeRange    TimeRange       `json:"time_range"`
    Context      SnapshotContext `json:"context"`
    Summary      string          `json:"summary"`
}

type TimeRange struct {
    Start time.Time `json:"start"`
    End   time.Time `json:"end"`
}

type SnapshotContext struct {
    AffectedModels   []string `json:"affected_models"`
    AffectedProviders []string `json:"affected_providers"`
    TotalRequests    int64    `json:"total_requests"`
    FailedRequests   int64    `json:"failed_requests"`
    ErrorBreakdown   map[string]int `json:"error_breakdown"`
}
```

### 2.3 修复策略引擎 (`fault/strategy.go`)

```go
package fault

import (
    "context"
    "log/slog"
    "time"
)

// Strategy 修复策略接口
type Strategy interface {
    // Name 策略名称
    Name() string

    // CanHandle 判断此策略能否处理该故障
    CanHandle(event *FaultEvent) bool

    // Execute 执行修复
    Execute(ctx context.Context, event *FaultEvent) error

    // Rollback 回滚修复（如果支持）
    Rollback(ctx context.Context, event *FaultEvent) error
}

// Engine 修复策略引擎
type Engine struct {
    strategies []Strategy
    store      Store
}

func NewEngine(store Store) *Engine {
    return &Engine{store: store}
}

func (e *Engine) RegisterStrategy(s Strategy) {
    e.strategies = append(e.strategies, s)
}

// Fix 执行修复：按优先级遍历策略，找到第一个可处理的执行
func (e *Engine) Fix(ctx context.Context, event *FaultEvent) error {
    for _, strategy := range e.strategies {
        if !strategy.CanHandle(event) {
            continue
        }

        slog.Info("applying fix strategy",
            "fault_id", event.ID,
            "strategy", strategy.Name(),
            "fault_type", event.FaultType,
        )

        start := time.Now()

        // 更新状态为修复中
        event.Status = FaultStatusFixing
        event.FixStrategy = &[]FixStrategy{FixStrategy(strategy.Name())}[0]
        e.store.UpdateEvent(ctx, event)

        // 执行修复
        if err := strategy.Execute(ctx, event); err != nil {
            slog.Error("fix strategy failed",
                "strategy", strategy.Name(),
                "error", err,
            )
            duration := int(time.Since(start).Milliseconds())
            event.FixResult = "failed: " + err.Error()
            event.FixDurationMs = &duration
            e.store.UpdateEvent(ctx, event)
            return err
        }

        // 修复成功
        duration := int(time.Since(start).Milliseconds())
        now := time.Now()
        event.Status = FaultStatusResolved
        event.ResolvedAt = &now
        event.FixResult = "success"
        event.FixDurationMs = &duration
        e.store.UpdateEvent(ctx, event)

        slog.Info("fix strategy applied successfully",
            "strategy", strategy.Name(),
            "fault_id", event.ID,
            "duration_ms", duration,
        )

        return nil
    }

    // 无匹配策略，升级为需要人工处理
    event.Status = FaultStatusEscalated
    event.FixResult = "no matching strategy"
    e.store.UpdateEvent(ctx, event)

    return nil
}
```

### 2.4 重启策略 (`fault/strategies/restart.go`)

```go
package strategies

import (
    "context"
    "fmt"
    "log/slog"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/kaixuan/llm-gateway-go/fault"
)

// RestartStrategy 重启故障组件策略
type RestartStrategy struct {
    db *pgxpool.Pool
}

func NewRestartStrategy(db *pgxpool.Pool) *RestartStrategy {
    return &RestartStrategy{db: db}
}

func (s *RestartStrategy) Name() string { return "restart" }

func (s *RestartStrategy) CanHandle(event *fault.FaultEvent) bool {
    // 适用于：凭据故障、熔断器打开
    return event.FaultType == fault.FaultCredentialFailure ||
           event.FaultType == fault.FaultCircuitOpen
}

func (s *RestartStrategy) Execute(ctx context.Context, event *fault.FaultEvent) error {
    if event.CredentialID == nil {
        return fmt.Errorf("credential_id required for restart strategy")
    }

    credID := *event.CredentialID
    slog.Info("restarting credential state", "credential_id", credID)

    // 1. 将凭据状态设为cooling
    _, err := s.db.Exec(ctx, `
        UPDATE credentials
        SET availability_state = 'cooling',
            availability_recover_at = now() + INTERVAL '2 minutes',
            state_updated_at = now(),
            state_reason_code = 'auto_restart'
        WHERE id = $1 AND lifecycle_status = 'active'
    `, credID)
    if err != nil {
        return fmt.Errorf("set cooling: %w", err)
    }

    // 2. 重置熔断器计数
    _, err = s.db.Exec(ctx, `
        UPDATE credentials
        SET consecutive_failures = 0,
            circuit_state = 'closed',
            cooling_until = NULL,
            state_updated_at = now()
        WHERE id = $1
    `, credID)
    if err != nil {
        return fmt.Errorf("reset circuit: %w", err)
    }

    // 3. 记录修复事件
    _, err = s.db.Exec(ctx, `
        INSERT INTO credential_state_log
            (credential_id, raw_model_name, state, updated_at, detail)
        SELECT
            $1, COALESCE(raw_model_name, 'unknown'), 'auto_restarted',
            now(), jsonb_build_object('fault_id', $2, 'action', 'restart')
        FROM model_probe_state
        WHERE credential_id = $1 AND state = 'broken_confirmed'
        LIMIT 5
    `, credID, event.ID)
    if err != nil {
        slog.Warn("failed to log restart to credential_state_log", "error", err)
    }

    return nil
}

func (s *RestartStrategy) Rollback(ctx context.Context, event *fault.FaultEvent) error {
    // 重启策略无需回滚（状态会在冷却后自动恢复）
    return nil
}
```

### 2.5 后台Worker (`fault/worker.go`)

```go
package fault

import (
    "context"
    "log/slog"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
)

// Worker 故障检测与自愈后台Worker
type Worker struct {
    db        *pgxpool.Pool
    engine    *Engine
    reporter  *Reporter // 向中心节点上报
    interval  time.Duration
    stopCh    chan struct{}
}

func NewWorker(db *pgxpool.Pool, engine *Engine, reporter *Reporter) *Worker {
    return &Worker{
        db:       db,
        engine:   engine,
        reporter: reporter,
        interval: 1 * time.Minute,
        stopCh:   make(chan struct{}),
    }
}

func (w *Worker) Start(ctx context.Context) {
    slog.Info("fault worker started", "interval", w.interval)
    go func() {
        ticker := time.NewTicker(w.interval)
        defer ticker.Stop()
        // 启动时立即执行一次
        w.tick(ctx)
        for {
            select {
            case <-ctx.Done():
                return
            case <-w.stopCh:
                return
            case <-ticker.C:
                w.tick(ctx)
            }
        }
    }()
}

func (w *Worker) Stop() {
    close(w.stopCh)
}

// tick 一次检测循环
func (w *Worker) tick(ctx context.Context) {
    stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()

    // 1. 检测高失败率
    w.detectHighFailureRate(stepCtx)

    // 2. 检测无可用候选
    w.detectNoCandidate(stepCtx)

    // 3. 检测高延迟
    w.detectHighLatency(stepCtx)

    // 4. 向中心节点上报
    if w.reporter != nil {
        w.reporter.ReportRecentEvents(stepCtx)
    }
}

// detectHighFailureRate 检测滑动窗口失败率
func (w *Worker) detectHighFailureRate(ctx context.Context) {
    rows, err := w.db.Query(ctx, `
        SELECT credential_id, raw_model_name,
               COUNT(*) as total,
               SUM(CASE WHEN state IN ('broken_confirmed', 'failing') THEN 1 ELSE 0 END) as failed
        FROM model_probe_state
        WHERE last_attempt_at > now() - INTERVAL '5 minutes'
        GROUP BY credential_id, raw_model_name
        HAVING COUNT(*) >= 10
           AND SUM(CASE WHEN state IN ('broken_confirmed', 'failing') THEN 1 ELSE 0 END)::float
                / COUNT(*) > 0.8
    `)
    if err != nil {
        slog.Error("detect high failure rate failed", "error", err)
        return
    }
    defer rows.Close()

    for rows.Next() {
        var credID int64
        var modelName string
        var total, failed int
        if err := rows.Scan(&credID, &modelName, &total, &failed); err != nil {
            continue
        }

        event := &FaultEvent{
            InstanceID:   getInstanceID(),
            FaultType:    FaultHighFailureRate,
            Severity:     SeverityMajor,
            Status:       FaultStatusDetected,
            CredentialID: &credID,
            ModelName:    modelName,
            DetectedAt:   time.Now(),
            ErrorDetails: fmt.Sprintf("failure rate %.1f%% (%d/%d) in 5min window",
                float64(failed)/float64(total)*100, failed, total),
        }

        // 保存事件
        if err := w.engine.store.CreateEvent(ctx, event); err != nil {
            continue
        }

        // 尝试自动修复
        if err := w.engine.Fix(ctx, event); err != nil {
            slog.Error("auto-fix failed", "fault_id", event.ID, "error", err)
        }
    }
}

// detectNoCandidate 检测无可用候选节点
func (w *Worker) detectNoCandidate(ctx context.Context) {
    // 查询没有可用凭据的模型
    rows, err := w.db.Query(ctx, `
        SELECT DISTINCT pm.raw_model_name, pm.provider_id
        FROM provider_models pm
        WHERE NOT EXISTS (
            SELECT 1
            FROM credential_model_bindings cmb
            JOIN credentials c ON c.id = cmb.credential_id
            WHERE cmb.provider_model_id = pm.id
              AND cmb.available = TRUE
              AND c.lifecycle_status = 'active'
              AND c.availability_state = 'ready'
        )
        AND pm.raw_model_name IS NOT NULL
    `)
    if err != nil {
        return
    }
    defer rows.Close()

    for rows.Next() {
        var modelName string
        var providerID int
        if err := rows.Scan(&modelName, &providerID); err != nil {
            continue
        }

        event := &FaultEvent{
            InstanceID:  getInstanceID(),
            FaultType:   FaultRouteError,
            Severity:    SeverityCritical,
            Status:      FaultStatusDetected,
            ModelName:   modelName,
            DetectedAt:  time.Now(),
            ErrorDetails: fmt.Sprintf("no available credentials for model %s (provider %d)", modelName, providerID),
        }

        w.engine.store.CreateEvent(ctx, event)
        // 路由错误通常无法自动修复，升级告警
        event.Status = FaultStatusEscalated
        w.engine.store.UpdateEvent(ctx, event)
    }
}

// detectHighLatency 检测P99延迟超阈值
func (w *Worker) detectHighLatency(ctx context.Context) {
    rows, err := w.db.Query(ctx, `
        SELECT credential_id, raw_model_name,
               AVG(last_latency_ms)::int as avg_latency
        FROM model_probe_state
        WHERE last_attempt_at > now() - INTERVAL '5 minutes'
          AND last_latency_ms > 5000  -- 超过5秒
        GROUP BY credential_id, raw_model_name
        HAVING AVG(last_latency_ms) > 10000  -- 平均超过10秒
    `)
    if err != nil {
        return
    }
    defer rows.Close()

    for rows.Next() {
        var credID int64
        var modelName string
        var avgLatency int
        if err := rows.Scan(&credID, &modelName, &avgLatency); err != nil {
            continue
        }

        event := &FaultEvent{
            InstanceID:   getInstanceID(),
            FaultType:    FaultHighLatency,
            Severity:     SeverityMinor,
            Status:       FaultStatusDetected,
            CredentialID: &credID,
            ModelName:    modelName,
            DetectedAt:   time.Now(),
            ErrorDetails: fmt.Sprintf("avg latency %dms in 5min window", avgLatency),
        }

        w.engine.store.CreateEvent(ctx, event)
    }
}
```

### 2.6 日志摘要生成 (`fault/analyzer.go`)

```go
package fault

import (
    "context"
    "fmt"
    "strings"
    "time"
)

// Analyzer 故障分析器
type Analyzer struct {
    store Store
}

// GenerateSummary 生成故障摘要（可调用LLM生成更智能的摘要）
func (a *Analyzer) GenerateSummary(ctx context.Context, event *FaultEvent) (string, error) {
    // 基础摘要：基于结构化数据
    var sb strings.Builder
    sb.WriteString(fmt.Sprintf("故障类型: %s\n", event.FaultType))
    sb.WriteString(fmt.Sprintf("严重程度: %s\n", event.Severity))
    sb.WriteString(fmt.Sprintf("检测时间: %s\n", event.DetectedAt.Format(time.RFC3339)))

    if event.CredentialID != nil {
        sb.WriteString(fmt.Sprintf("凭据ID: %d\n", *event.CredentialID))
    }
    if event.ModelName != "" {
        sb.WriteString(fmt.Sprintf("模型: %s\n", event.ModelName))
    }
    if event.ErrorDetails != "" {
        sb.WriteString(fmt.Sprintf("错误详情: %s\n", event.ErrorDetails))
    }

    // 修复结果
    if event.FixStrategy != nil {
        sb.WriteString(fmt.Sprintf("修复策略: %s\n", *event.FixStrategy))
    }
    if event.FixResult != "" {
        sb.WriteString(fmt.Sprintf("修复结果: %s\n", event.FixResult))
    }

    return sb.String(), nil
}

// GenerateBatchSummary 批量故障摘要（用于回传中心节点）
func (a *Analyzer) GenerateBatchSummary(ctx context.Context, instanceID string, since time.Duration) (*FaultSnapshot, error) {
    events, err := a.store.ListEventsByTimeRange(ctx, instanceID, time.Now().Add(-since), time.Now())
    if err != nil {
        return nil, err
    }

    snapshot := &FaultSnapshot{
        Events: events,
        TimeRange: TimeRange{
            Start: time.Now().Add(-since),
            End:   time.Now(),
        },
        Context: SnapshotContext{
            AffectedModels: extractModels(events),
        },
    }

    // 生成综合摘要
    summary, _ := a.GenerateSummary(ctx, &FaultEvent{
        FaultType:    FaultType("batch"),
        Severity:     SeverityInfo,
        ErrorDetails: fmt.Sprintf("共检测到 %d 个故障事件", len(events)),
    })
    snapshot.Summary = summary

    return snapshot, nil
}

func extractModels(events []FaultEvent) []string {
    seen := make(map[string]bool)
    var result []string
    for _, e := range events {
        if e.ModelName != "" && !seen[e.ModelName] {
            seen[e.ModelName] = true
            result = append(result, e.ModelName)
        }
    }
    return result
}
```

### 2.7 数据库Schema (迁移文件 `startup/368_fault_management.sql`)

```sql
-- 368_fault_management.sql

-- 故障事件表
CREATE TABLE IF NOT EXISTS fault_events (
    id              BIGSERIAL PRIMARY KEY,
    instance_id     TEXT NOT NULL,
    fault_type      TEXT NOT NULL,
    severity        TEXT NOT NULL DEFAULT 'minor'
        CHECK (severity IN ('critical', 'major', 'minor', 'info')),
    status          TEXT NOT NULL DEFAULT 'detected'
        CHECK (status IN ('detected', 'analyzing', 'fixing', 'resolved', 'escalated')),
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    credential_id   BIGINT,
    model_name      TEXT,
    provider_name   TEXT,
    root_cause      TEXT,
    error_details   TEXT,
    raw_logs        TEXT,
    summary         TEXT,
    fix_strategy    TEXT,
    fix_applied     TEXT,
    fix_result      TEXT,
    fix_duration_ms INT,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_fe_instance_ts ON fault_events (instance_id, detected_at DESC);
CREATE INDEX IF NOT EXISTS idx_fe_type_status ON fault_events (fault_type, status);
CREATE INDEX IF NOT EXISTS idx_fe_severity ON fault_events (severity, detected_at DESC)
    WHERE status IN ('detected', 'analyzing', 'fixing');
CREATE INDEX IF NOT EXISTS idx_fe_unresolved ON fault_events (detected_at DESC)
    WHERE status NOT IN ('resolved', 'escalated');
```

---

## 三、自动升级模块

### 3.1 目录结构

```
autoupdate/                       # 新增顶层包
├── types.go                      # 版本信息类型
├── checker.go                    # 版本检查器
├── downloader.go                 # 下载器（含校验）
├── updater.go                    # 升级执行器
├── rollout.go                    # 灰度发布策略
├── worker.go                     # 后台Worker（定时检查）
├── admin_api.go                  # Admin API（发布新版本）
└── store.go                      # 存储接口
```

### 3.2 核心类型 (`autoupdate/types.go`)

```go
package autoupdate

import "time"

// VersionInfo 版本信息
type VersionInfo struct {
    Version      string `json:"version"`       // 语义化版本: 2.5.0
    BuildHash    string `json:"build_hash"`    // Git commit hash
    BuildTime    string `json:"build_time"`    // 构建时间
    BinaryURL    string `json:"binary_url"`    // 下载URL
    BinarySize   int64  `json:"binary_size"`   // 二进制大小
    Checksum     string `json:"checksum"`      // SHA256
    Signature    string `json:"signature"`      // RSA签名 (base64)
    ReleaseNotes string `json:"release_notes"`
    MinVersion   string `json:"min_version"`   // 最低支持版本（强制升级）
    ReleasedAt   time.Time `json:"released_at"`
}

// RolloutConfig 灰度发布配置
type RolloutConfig struct {
    Phase       int       `json:"phase"`        // 0=测试, 1=5%, 2=20%, 3=50%, 4=100%
    Percentage  float64   `json:"percentage"`   // 目标百分比
    StartTime   time.Time `json:"start_time"`   // 阶段开始时间
    MinVersion  string    `json:"min_version"`  // 强制升级版本
}

// UpdateResult 升级结果
type UpdateResult struct {
    Success     bool   `json:"success"`
    OldVersion  string `json:"old_version"`
    NewVersion  string `json:"new_version"`
    Error       string `json:"error,omitempty"`
    DurationMs  int    `json:"duration_ms"`
}

// CheckResponse 版本检查响应
type CheckResponse struct {
    NeedUpgrade  bool          `json:"need_upgrade"`
    Current      string        `json:"current_version"`
    Latest       *VersionInfo  `json:"latest,omitempty"`
    Force        bool          `json:"force"`         // 强制升级
    Message      string        `json:"message,omitempty"`
}
```

### 3.3 灰度策略 (`autoupdate/rollout.go`)

```go
package autoupdate

import (
    "hash/crc32"
    "math"
)

// RolloutPhases 灰度阶段定义
var RolloutPhases = []RolloutConfig{
    {Phase: 1, Percentage: 0.05},  // 5% - 内部测试
    {Phase: 2, Percentage: 0.20},  // 20% - 早期用户
    {Phase: 3, Percentage: 0.50},  // 50% - 多数用户
    {Phase: 4, Percentage: 1.00},  // 100% - 全量推送
}

// IsMyTurn 判断当前实例是否在灰度范围内
func (rc *RolloutConfig) IsMyTurn(instanceID string) bool {
    if rc.Percentage >= 1.0 {
        return true
    }
    hash := crc32.ChecksumIEEE([]byte(instanceID))
    threshold := int(float64(math.MaxUint32) * rc.Percentage)
    return int(hash) < threshold
}

// NextPhase 获取下一个灰度阶段
func NextPhase(current int) *RolloutConfig {
    for _, p := range RolloutPhases {
        if p.Phase == current+1 {
            return &p
        }
    }
    return nil // 已经是最终阶段
}
```

### 3.4 升级执行器 (`autoupdate/updater.go`)

```go
package autoupdate

import (
    "context"
    "crypto/sha256"
    "crypto/rsa"
    "encoding/base64"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "time"

    "github.com/inconshreveable/go-update"
)

// Updater 升级执行器
type Updater struct {
    serverURL    string
    publicKey    *rsa.PublicKey
    instanceID   string
    currentPath  string // 当前可执行文件路径
    httpClient   *http.Client
}

func NewUpdater(serverURL string, publicKey *rsa.PublicKey, instanceID string) (*Updater, error) {
    exe, err := os.Executable()
    if err != nil {
        return nil, err
    }
    return &Updater{
        serverURL:   serverURL,
        publicKey:   publicKey,
        instanceID:  instanceID,
        currentPath: exe,
        httpClient:  &http.Client{Timeout: 5 * time.Minute},
    }, nil
}

// CheckForUpdate 检查是否有新版本
func (u *Updater) CheckForUpdate(ctx context.Context, currentVersion string) (*CheckResponse, error) {
    url := fmt.Sprintf("%s/api/autoupdate/check?version=%s&instance=%s&os=%s&arch=%s",
        u.serverURL, currentVersion, u.instanceID, runtime.GOOS, runtime.GOARCH)

    resp, err := u.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var checkResp CheckResponse
    if err := readJSON(resp, &checkResp); err != nil {
        return nil, err
    }
    return &checkResp, nil
}

// ApplyUpdate 执行升级
func (u *Updater) ApplyUpdate(ctx context.Context, info *VersionInfo) (*UpdateResult, error) {
    start := time.Now()
    result := &UpdateResult{
        OldVersion: getCurrentVersion(),
    }

    // 1. 下载新二进制
    slog.Info("downloading new version", "version", info.Version, "url", info.BinaryURL)
    resp, err := u.httpClient.Get(info.BinaryURL)
    if err != nil {
        result.Error = fmt.Sprintf("download failed: %v", err)
        return result, err
    }
    defer resp.Body.Close()

    newData, err := io.ReadAll(resp.Body)
    if err != nil {
        result.Error = fmt.Sprintf("read response: %v", err)
        return result, err
    }

    // 2. 校验Checksum
    hash := sha256.Sum256(newData)
    checksum := fmt.Sprintf("%x", hash)
    if checksum != info.Checksum {
        result.Error = "checksum mismatch"
        return result, errors.New("checksum verification failed")
    }

    // 3. 校验RSA签名
    sigBytes, err := base64.StdEncoding.DecodeString(info.Signature)
    if err != nil {
        result.Error = "invalid signature encoding"
        return result, err
    }
    if err := rsa.VerifyPKCS1v15(u.publicKey, 0x0b, hash[:], sigBytes); err != nil {
        result.Error = "signature verification failed"
        return result, err
    }

    // 4. 备份当前版本
    backupPath := u.currentPath + ".bak"
    if err := copyFile(u.currentPath, backupPath); err != nil {
        result.Error = fmt.Sprintf("backup failed: %v", err)
        return result, err
    }

    // 5. 应用更新
    if err := update.Apply(bytes.NewReader(newData), update.Options{}); err != nil {
        // 回滚
        os.Rename(backupPath, u.currentPath)
        result.Error = fmt.Sprintf("apply update: %v", err)
        return result, err
    }

    // 6. 更新版本文件
    os.WriteFile("VERSION", []byte(info.Version+"\n"), 0644)

    result.Success = true
    result.NewVersion = info.Version
    result.DurationMs = int(time.Since(start).Milliseconds())

    slog.Info("update applied successfully", "version", info.Version, "duration_ms", result.DurationMs)

    return result, nil
}

// Restart 重启进程
func (u *Updater) Restart() error {
    exe, err := os.Executable()
    if err != nil {
        return err
    }
    cmd := exec.Command(exe, os.Args[1:]...)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    if err := cmd.Start(); err != nil {
        return err
    }
    os.Exit(0)
    return nil
}
```

### 3.5 数据库Schema (迁移文件 `startup/369_autoupdate.sql`)

```sql
-- 369_autoupdate.sql

-- 版本发布记录
CREATE TABLE IF NOT EXISTS release_versions (
    id              BIGSERIAL PRIMARY KEY,
    version         TEXT NOT NULL UNIQUE,
    build_hash      TEXT NOT NULL,
    build_time      TEXT,
    binary_url      TEXT NOT NULL,
    binary_size     BIGINT,
    checksum        TEXT NOT NULL,
    signature       TEXT NOT NULL,
    release_notes   TEXT,
    min_version     TEXT,
    released_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_by     TEXT,
    rollout_phase   INT NOT NULL DEFAULT 0,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE
);
CREATE INDEX IF NOT EXISTS rv_version ON release_versions (version);
CREATE INDEX IF NOT EXISTS rv_released ON release_versions (released_at DESC);

-- 升级记录
CREATE TABLE IF NOT EXISTS upgrade_log (
    id              BIGSERIAL PRIMARY KEY,
    instance_id     TEXT NOT NULL,
    old_version     TEXT NOT NULL,
    new_version     TEXT NOT NULL,
    status          TEXT NOT NULL,  -- downloading, applying, success, failed
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    duration_ms     INT,
    error_message   TEXT
);
CREATE INDEX IF NOT EXISTS ul_instance ON upgrade_log (instance_id, started_at DESC);
```

---

## 四、中心运维平台

### 4.1 目录结构

```
center/                           # 中心运维平台
├── types.go                      # 通信协议类型
├── registry.go                   # 实例注册与心跳
├── heartbeat.go                  # 心跳Worker
├── config_push.go                # 远程配置下发
├── instance_monitor.go           # 实例监控聚合
├── admin_api.go                  # 超级管理员API
├── dashboard_api.go              # Dashboard数据接口
└── worker.go                     # 后台聚合Worker
```

### 4.2 核心通信协议 (`center/types.go`)

```go
package center

import "time"

// InstanceRegistration 实例注册请求
type InstanceRegistration struct {
    InstanceID  string `json:"instance_id"`
    Hostname    string `json:"hostname"`
    IPAddress   string `json:"ip_address"`
    Version     string `json:"version"`
    LicenseKey  string `json:"license_key"`
    OS          string `json:"os"`
    Arch        string `json:"arch"`
    StartedAt   time.Time `json:"started_at"`
}

// Heartbeat 实例心跳
type Heartbeat struct {
    InstanceID     string          `json:"instance_id"`
    Timestamp      time.Time       `json:"timestamp"`
    Status         string          `json:"status"` // healthy, degraded, down
    UptimeSec      int64           `json:"uptime_sec"`
    RequestCount   int64           `json:"request_count"`
    ErrorCount     int64           `json:"error_count"`
    Version        string          `json:"version"`
    Metrics        InstanceMetrics `json:"metrics"`
    FaultEvents    []FaultReport   `json:"fault_events,omitempty"` // 本次上报的故障事件
    PendingConfig  *ConfigPush     `json:"pending_config,omitempty"` // 中心下发的配置
}

// InstanceMetrics 实例指标
type InstanceMetrics struct {
    CPUUsage       float64 `json:"cpu_usage"`
    MemoryUsage    float64 `json:"memory_usage"`
    Goroutines     int     `json:"goroutines"`
    AvgLatencyMs   int     `json:"avg_latency_ms"`
    P95LatencyMs   int     `json:"p95_latency_ms"`
    ActiveConns    int     `json:"active_conns"`
    PoolActive     int     `json:"pool_active"`
    PoolIdle       int     `json:"pool_idle"`
}

// FaultReport 故障上报
type FaultReport struct {
    ID          int64  `json:"id"`
    FaultType   string `json:"fault_type"`
    Severity    string `json:"severity"`
    ModelName   string `json:"model_name"`
    Summary     string `json:"summary"`
    DetectedAt  time.Time `json:"detected_at"`
}

// ConfigPush 远程配置下发
type ConfigPush struct {
    ConfigVersion string         `json:"config_version"`
    ConfigData    map[string]any `json:"config_data"`
    Applied       bool           `json:"applied"`
    AppliedAt     *time.Time     `json:"applied_at,omitempty"`
    Error         string         `json:"error,omitempty"`
}
```

### 4.3 数据库Schema (迁移文件 `startup/370_center_ops.sql`)

```sql
-- 370_center_ops.sql

-- 注册实例表
CREATE TABLE IF NOT EXISTS gateway_instances (
    instance_id     TEXT PRIMARY KEY,
    hostname        TEXT NOT NULL,
    ip_address      TEXT NOT NULL,
    version         TEXT NOT NULL,
    license_key     TEXT,
    os              TEXT,
    arch            TEXT,
    status          TEXT NOT NULL DEFAULT 'unknown'
        CHECK (status IN ('healthy', 'degraded', 'down', 'unknown')),
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat  TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS gi_status ON gateway_instances (status);
CREATE INDEX IF NOT EXISTS gi_license ON gateway_instances (license_key);
CREATE INDEX IF NOT EXISTS gi_heartbeat ON gateway_instances (last_heartbeat DESC);

-- 实例心跳历史（用于趋势分析）
CREATE TABLE IF NOT EXISTS instance_heartbeats (
    instance_id     TEXT NOT NULL,
    ts              TIMESTAMPTZ NOT NULL DEFAULT now(),
    status          TEXT NOT NULL,
    uptime_sec      BIGINT,
    request_count   BIGINT,
    error_count     BIGINT,
    metrics         JSONB,
    fault_count     INT DEFAULT 0,
    PRIMARY KEY (instance_id, ts)
);
-- 分区表：按月分区
-- ALTER TABLE instance_heartbeats PARTITION BY RANGE (ts);

-- 配置下发记录
CREATE TABLE IF NOT EXISTS config_push_log (
    id              BIGSERIAL PRIMARY KEY,
    config_version  TEXT NOT NULL,
    target          TEXT NOT NULL,  -- 'all' 或 instance_id
    config_data     JSONB NOT NULL,
    pushed_by       TEXT,
    pushed_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    ack_count       INT DEFAULT 0,
    nack_count      INT DEFAULT 0
);
```

---

## 五、模块间依赖关系

```
                    ┌─────────────┐
                    │  center     │ 中心运维平台
                    │ (控制中心)   │
                    └──────┬──────┘
                           │
           ┌───────────────┼───────────────┐
           │               │               │
    ┌──────▼──────┐ ┌──────▼──────┐ ┌──────▼──────┐
    │  license    │ │  fault      │ │ autoupdate  │
    │  (许可证)   │ │ (故障自愈)  │ │ (自动升级)  │
    └──────┬──────┘ └──────┬──────┘ └──────┬──────┘
           │               │               │
           └───────────────┼───────────────┘
                           │
                    ┌──────▼──────┐
                    │  Gateway    │ 现有核心网关
                    │  (数据面)   │
                    └─────────────┘
```

**依赖方向（单向，无循环）:**
- `gateway` → `fault`（引用故障事件写入）
- `gateway` → `license`（引用License验证）
- `gateway` → `autoupdate`（引用版本检查）
- `fault` → `center`（上报故障事件）
- `license` → `center`（注册设备）
- `autoupdate` → `center`（检查版本）
- `center` → `fault/license/autoupdate`（下发命令）

**避免循环导入的关键设计:**
- 所有跨模块通信通过 **接口** + **Event Bus**
- `center` 模块不直接依赖其他三个模块的包
- 使用 `eventbus.MemoryBus` 解耦

---

## 六、详细任务清单（可执行级）

### Phase 1: 基础框架 (5天)

| # | 任务 | 预估 | 依赖 | 输出 |
|---|------|------|------|------|
| 1.1 | 创建 `license/types.go` - 定义核心类型 | 0.5d | 无 | 类型定义 |
| 1.2 | 创建 `license/fingerprint.go` - 硬件指纹 | 1d | 无 | 指纹生成+匹配 |
| 1.3 | 创建 `license/crypto.go` - 加密签名 | 1d | 1.1 | RSA/AES/JWT |
| 1.4 | 创建 `fault/types.go` - 故障事件类型 | 0.5d | 无 | 类型定义 |
| 1.5 | 创建 `autoupdate/types.go` - 版本类型 | 0.5d | 无 | 类型定义 |
| 1.6 | 创建 `center/types.go` - 通信协议 | 0.5d | 无 | 协议定义 |
| 1.7 | 编写SQL迁移文件 367-370 | 1d | 无 | 数据库表 |
| 1.8 | 更新 `db/db.go` - 添加ensure方法 | 0.5d | 1.7 | Schema应用 |

### Phase 2: License模块 (8天)

| # | 任务 | 预估 | 依赖 | 输出 |
|---|------|------|------|------|
| 2.1 | 创建 `license/store.go` + `store_pgx.go` | 1.5d | 1.1, 1.7 | 存储层 |
| 2.2 | 创建 `license/device_manager.go` - 2设备管理 | 1.5d | 2.1 | 设备管理 |
| 2.3 | 创建 `license/validator.go` - License验证 | 1d | 1.3, 2.1 | 验证器 |
| 2.4 | 创建 `license/activator.go` - 在线激活 | 1.5d | 2.2, 2.3 | 激活客户端 |
| 2.5 | 创建 `license/offline.go` - 离线激活 | 1d | 2.2 | 离线流程 |
| 2.6 | 创建 `license/admin_api.go` - Admin API | 1.5d | 2.1-2.5 | HTTP Handlers |
| 2.7 | 更新 `cmd/gateway/main.go` - 注册License模块 | 0.5d | 2.6 | 集成 |
| 2.8 | 编写单元测试 | 1d | 2.1-2.6 | 测试覆盖 |

### Phase 3: 故障自愈模块 (7天)

| # | 任务 | 预估 | 依赖 | 输出 |
|---|------|------|------|------|
| 3.1 | 创建 `fault/store.go` + `store_pgx.go` | 1d | 1.4, 1.7 | 存储层 |
| 3.2 | 创建 `fault/strategies/restart.go` | 1d | 3.1 | 重启策略 |
| 3.3 | 创建 `fault/strategies/failover.go` | 1d | 3.1 | 切换策略 |
| 3.4 | 创建 `fault/strategies/rollback.go` | 1d | 3.1 | 回滚策略 |
| 3.5 | 创建 `fault/strategy.go` - 策略引擎 | 1d | 3.2-3.4 | 引擎 |
| 3.6 | 创建 `fault/worker.go` - 检测Worker | 1d | 3.5 | 后台Worker |
| 3.7 | 创建 `fault/analyzer.go` + `reporter.go` | 0.5d | 3.1 | 分析+上报 |
| 3.8 | 创建 `fault/admin_api.go` - Admin API | 0.5d | 3.1 | HTTP Handlers |
| 3.9 | 编写单元测试 | 1d | 3.1-3.8 | 测试覆盖 |

### Phase 4: 自动升级模块 (5天)

| # | 任务 | 预估 | 依赖 | 输出 |
|---|------|------|------|------|
| 4.1 | 创建 `autoupdate/store.go` + `store_pgx.go` | 1d | 1.5, 1.7 | 存储层 |
| 4.2 | 创建 `autoupdate/rollout.go` - 灰度策略 | 0.5d | 无 | 灰度判断 |
| 4.3 | 创建 `autoupdate/updater.go` - 升级执行 | 1.5d | 4.1, 4.2 | 升级器 |
| 4.4 | 创建 `autoupdate/worker.go` - 定时检查 | 1d | 4.3 | 后台Worker |
| 4.5 | 创建 `autoupdate/admin_api.go` - 发布管理 | 1d | 4.1 | HTTP Handlers |
| 4.6 | 编写单元测试 | 1d | 4.1-4.5 | 测试覆盖 |

### Phase 5: 中心运维平台 (7天)

| # | 任务 | 预估 | 依赖 | 输出 |
|---|------|------|------|------|
| 5.1 | 创建 `center/registry.go` - 实例注册 | 1d | 1.6, 1.7 | 注册中心 |
| 5.2 | 创建 `center/heartbeat.go` - 心跳Worker | 1.5d | 5.1 | 心跳系统 |
| 5.3 | 创建 `center/config_push.go` - 配置下发 | 1d | 5.1 | 配置推送 |
| 5.4 | 创建 `center/instance_monitor.go` - 监控聚合 | 1d | 5.2 | 监控聚合 |
| 5.5 | 创建 `center/dashboard_api.go` - Dashboard | 1.5d | 5.1-5.4 | Dashboard API |
| 5.6 | 创建 `center/admin_api.go` - 管理API | 0.5d | 5.1-5.4 | 管理API |
| 5.7 | 更新 `cmd/gateway/main.go` - 完整集成 | 1d | 全部 | 系统集成 |
| 5.8 | 编写集成测试 | 1.5d | 全部 | 测试 |

### Phase 6: 前端界面 (8天)

| # | 任务 | 预估 | 依赖 | 输出 |
|---|------|------|------|------|
| 6.1 | License管理页面 | 2d | Phase 2 | Admin UI |
| 6.2 | 实例监控仪表盘 | 2d | Phase 5 | Dashboard UI |
| 6.3 | 故障事件查看页面 | 1d | Phase 3 | Fault UI |
| 6.4 | 版本发布管理页面 | 1d | Phase 4 | Release UI |
| 6.5 | 配置下发管理页面 | 1d | Phase 5 | Config UI |
| 6.6 | 网关侧License申请页面 | 1d | Phase 2 | Gateway UI |

---

## 七、API端点完整清单

### License API
| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| POST | /api/admin/licenses | superAdmin | 创建License |
| GET | /api/admin/licenses | superAdmin | 列出License |
| GET | /api/admin/licenses/:key | superAdmin | License详情 |
| DELETE | /api/admin/licenses/:key | superAdmin | 撤销License |
| GET | /api/admin/licenses/:key/devices | superAdmin | 设备列表 |
| POST | /api/admin/licenses/:key/deactivate | superAdmin | 停用设备 |
| GET | /api/admin/offline-requests | superAdmin | 离线请求列表 |
| POST | /api/admin/offline-requests/:id/approve | superAdmin | 审批通过 |
| POST | /api/admin/offline-requests/:id/reject | superAdmin | 审批拒绝 |

### 网关License API
| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| POST | /api/license/activate | 无 | 在线激活 |
| POST | /api/license/deactivate | JWT | 停用设备 |
| POST | /api/license/heartbeat | JWT | 心跳保活 |
| POST | /api/license/offline-request | 无 | 生成离线请求 |
| POST | /api/license/import-response | 无 | 导入离线响应 |
| GET | /api/license/status | JWT | License状态 |

### 故障管理 API
| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| GET | /api/admin/faults | superAdmin | 故障事件列表 |
| GET | /api/admin/faults/:id | superAdmin | 故障详情 |
| POST | /api/admin/faults/:id/fix | superAdmin | 手动触发修复 |
| GET | /api/admin/faults/summary | superAdmin | 故障摘要统计 |

### 自动升级 API
| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| POST | /api/admin/releases | superAdmin | 发布新版本 |
| GET | /api/admin/releases | superAdmin | 版本列表 |
| GET | /api/admin/releases/latest | superAdmin | 最新版本 |
| GET | /api/autoupdate/check | 无 | 检查更新 |
| GET | /api/autoupdate/download/:version | 无 | 下载二进制 |

### 中心运维 API
| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| POST | /api/center/register | 无 | 实例注册 |
| POST | /api/center/heartbeat | 无 | 心跳上报 |
| GET | /api/admin/instances | superAdmin | 实例列表 |
| GET | /api/admin/instances/:id | superAdmin | 实例详情 |
| GET | /api/admin/instances/:id/heartbeats | superAdmin | 心跳历史 |
| POST | /api/admin/config-push | superAdmin | 远程配置下发 |

---

## 八、风险与缓解

| 风险 | 影响 | 缓解 | 优先级 |
|------|------|------|--------|
| License被破解 | 高 | 7层防护（RSA+AES+指纹+JWT+时间+混淆+完整性） | P0 |
| 升级失败服务中断 | 高 | 备份+自动回滚+灰度发布 | P0 |
| 网络分区激活失败 | 中 | 离线激活备选+本地缓存 | P1 |
| 硬件变更指纹失效 | 中 | 模糊匹配阈值0.6+用户可重新激活 | P1 |
| 中心节点单点故障 | 高 | 多实例部署+本地缓存License+心跳断线重连 | P0 |
| 循环导入 | 中 | 接口定义在消费方+Event Bus解耦 | P1 |
| 数据库Schema冲突 | 低 | 迁移编号严格递增+幂等SQL | P2 |
