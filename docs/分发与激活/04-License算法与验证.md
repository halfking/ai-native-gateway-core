# 04. License 算法与验证

## 一、算法选型

| 用途 | 算法 | 选型理由 |
|------|------|---------|
| 签名 | **RSA-2048** | 现有实现稳定；签名/验证快；兼容性好 |
| 对称加密 | **AES-256-GCM** | AEAD，自带认证 |
| 哈希 | **SHA-256** | 行业标准 |
| 机器指纹 | **多维拼接 + SHA-256** | 防克隆 |

未来可平滑迁移到 Ed25519（更小、更快），架构已预留接口。

## 二、License 数据结构

```go
// licensing/types.go（已实现）
type License struct {
    ID               int64      `json:"id"`
    LicenseKey       string     `json:"license_key"`
    CustomerName     string     `json:"customer_name"`
    CustomerEmail    string     `json:"customer_email"`
    MaxDevices       int        `json:"max_devices"`
    SubscriptionTier string     `json:"subscription_tier"`  // community/trial/standard/professional/enterprise
    Features         []string   `json:"features"`
    ExpiresAt        time.Time  `json:"expires_at"`
    CreatedAt        time.Time  `json:"created_at"`
    RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

type SignedLicense struct {
    Data      []byte `json:"data"`       // JSON 序列化的 License
    Signature []byte `json:"signature"`  // RSA-PKCS1v15-SHA256 签名
}
```

## 三、License Key 编码

格式：`KXGW-XXXX-XXXX-XXXX`（25 字符）

```
KXGW-A2B3C4D5-E6F7G8H9-J0K1L2M3-X7Y8
└──┘ └────────┘ └────────┘ └────────┘ └──┘
 │      │          │          │       └─ 4 位 Luhn 校验码
 │      │          │          └── 8 位随机（实例 ID 衍生）
 │      │          └── 8 位随机（批次衍生）
 │      └── 8 位随机（seed 衍生）
 └── 公司标识
```

**生成规则**：
1. 32 字节随机种子（CSPRNG）
2. 拆分为 3 段各 8 字符 base32
3. Luhn 算法计算 4 位校验码
4. 拼接成 25 字符

## 四、签名与验证流程

### 4.1 服务端签发（主控端）

```go
// licensing/crypto.go（已实现）
func (c *CryptoConfig) SignLicense(lic *License) (*SignedLicense, error) {
    data, _ := json.Marshal(lic)
    hash := sha256.Sum256(data)
    signature, _ := rsa.SignPKCS1v15(rand.Reader, c.PrivateKey, crypto.SHA256, hash[:])
    return &SignedLicense{Data: data, Signature: signature}, nil
}
```

### 4.2 客户端验证

```go
// licensing/crypto.go（已实现）
func (c *CryptoConfig) VerifyLicense(signed *SignedLicense) (*License, error) {
    hash := sha256.Sum256(signed.Data)
    if err := rsa.VerifyPKCS1v15(c.PublicKey, crypto.SHA256, hash[:], signed.Signature); err != nil {
        return nil, errors.New("license signature invalid")
    }
    var lic License
    json.Unmarshal(signed.Data, &lic)
    return &lic, nil
}
```

## 五、机器指纹（已实现 + 待增强）

### 5.1 当前字段

```go
// licensing/fingerprint.go
type Fingerprint struct {
    MachineID  string `json:"machine_id"`   // /etc/machine-id
    CPUInfo    string `json:"cpu_info"`     // CPU model name
    HostID     string `json:"host_id"`      // hostinfo
    OS         string `json:"os"`
    Arch       string `json:"arch"`
    PrimaryMAC string `json:"primary_mac"` // 第一块网卡 MAC
}

func (fp *Fingerprint) Hash() string {
    raw := fmt.Sprintf("%s|%s|%s|%s", fp.MachineID, fp.CPUInfo, fp.HostID, fp.PrimaryMAC)
    hash := sha256.Sum256([]byte(raw))
    return fmt.Sprintf("%x", hash[:16])  // 32 字符
}
```

### 5.2 模糊匹配（防硬件微变）

```go
func (fp *Fingerprint) MatchScore(stored *Fingerprint) float64 {
    // 权重：machineid 3 + cpu 2 + hostid 1 + mac 1 = 7
    score := 0.0
    if fp.MachineID == stored.MachineID { score += 3.0 }
    if fp.CPUInfo == stored.CPUInfo { score += 2.0 }
    if fp.HostID == stored.HostID { score += 1.0 }
    if fp.PrimaryMAC == stored.PrimaryMAC { score += 1.0 }
    return score / 7.0
}

const MatchThreshold = 0.6  // 60% 匹配即认为同一台机器
```

### 5.3 待增强字段（M1-C11 任务）

```go
type Fingerprint struct {
    // ... 现有字段
    
    // 新增（防克隆 / 防虚拟机漂移）
    DiskSerial   string `json:"disk_serial"`   // lsblk --nodeps -no serial
    BIOSUUID     string `json:"bios_uuid"`     // /sys/class/dmi/id/product_uuid
    BootID       string `json:"boot_id"`       // /proc/sys/kernel/random/boot_id（每次启动变）
    VirtType     string `json:"virt_type"`     // kvm/vmware/docker/baremetal
    CloudVendor  string `json:"cloud_vendor"`  // aws/gcp/azure/alibaba
    ContainerID  string `json:"container_id"`  // docker 场景下
}
```

**哈希时排除易变字段**（BootID 不参与哈希，但用于调试）。

## 六、时钟防回拨（M1-C10）

```go
// licensing/clock.go（待实现）

type ClockGuard struct {
    storage     ClockStorage
    tolerance   time.Duration  // 默认 5 分钟
}

func (g *ClockGuard) ValidateLicense() error {
    now := time.Now()
    
    // 1. 检查启动时间倒退
    lastBoot, err := g.storage.LastBootTime()
    if err == nil && now.Before(lastBoot) {
        return fmt.Errorf("clock rollback detected")
    }
    
    // 2. 检查 license 验证时间倒退
    lastVerify, err := g.storage.LastKnownLicenseVerify()
    if err == nil && now.Before(lastVerify.Add(-g.tolerance)) {
        return fmt.Errorf("license verify time rolled back")
    }
    
    return nil
}
```

## 七、License 验证时机

| 时机 | 动作 | 失败行为 |
|------|------|---------|
| 启动时 | 强制验证 + 反调试 | 拒绝启动 |
| 每 24h | 验证 + 心跳上报 | 进入降级模式 |
| 关键操作（创建租户等） | 实时验证 | 拒绝操作 |
| License 即将到期 | 启动提醒 | 仅警告 |

## 八、License 缓存策略

```go
// licensing/validator.go（已实现）
type validatorCache struct {
    mu    sync.RWMutex
    items map[string]*cachedLicense
}

type cachedLicense struct {
    license   *License
    expiresAt time.Time
    lastCheck time.Time
}

func (v *Validator) ValidateLicense(ctx context.Context, licenseKey string) (*License, error) {
    cached := v.cache.get(licenseKey)
    if cached != nil && time.Since(cached.lastCheck) < 5*time.Minute {
        // 命中缓存，5 分钟内不重查
        if time.Now().After(cached.license.ExpiresAt) {
            return nil, ErrLicenseExpired
        }
        return cached.license, nil
    }
    // 重新从 DB 查询
    lic, err := v.store.GetLicense(ctx, licenseKey)
    // ...
    v.cache.put(licenseKey, lic)
    return lic, nil
}
```

## 九、双版本 License 强制策略

| 版本 | 强制策略 | 实现 |
|------|---------|------|
| **Master** | ❌ 跳过 | `licensing.SkipEnforcement()` |
| **Customer** | ✅ 强制 | `licensing.EnforceAtStartup()` |

```go
// cmd/gateway-master/main.go
// +build master
func main() {
    licensing.SkipEnforcement()
    // ...
}

// cmd/gateway-client/main.go
// +build customer
func main() {
    if err := licensing.EnforceAtStartup(); err != nil {
        log.Fatalf("license validation failed: %v", err)
    }
    // ...
}
```

## 十、防破解加固（M3-C11/C12/C13）

| 攻击 | 防御 | 实现位置 |
|------|------|---------|
| 复制 license.dat | 机器指纹校验 | licensing/fingerprint.go ✅ |
| 篡改 license.dat | RSA 签名验证 | licensing/crypto.go ✅ |
| 时间回拨 | 持久化时钟 | licensing/clock.go（M1-C10） |
| 重放攻击 | nonce + 服务端已用集合 | licensing/nonce.go（M3-C13） |
| Patch 二进制 | 完整性自校验 | licensing/antitamper.go（M3-C11） |
| 调试器附加 | ptrace 检测 | licensing/antidebug.go（M3-C12） |
| 克隆虚拟机 | Disk Serial + BIOS UUID | licensing/enhanced_fingerprint.go（M1-C11） |