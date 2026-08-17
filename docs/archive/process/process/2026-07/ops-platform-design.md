# 运维平台与License管理系统 - 技术方案

> 基于您的需求：故障自动检测与修复、自动升级、License管理（最多2设备）、中心化运维平台

## 一、系统架构总览

```
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│                           中心化运维平台 (Control Center)                                 │
├─────────────────────────────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐ │
│  │ 实时监控    │  │ 故障自愈    │  │ 自动升级    │  │ License管理  │  │ 日志分析    │ │
│  │ 仪表盘      │  │ 引擎        │  │ 管理        │  │              │  │              │ │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘ │
│           │               │               │               │               │            │
│           └───────────────┴───────────────┴───────────────┴───────────────┘            │
│                                       │                                                 │
│                              ┌────────▼────────┐                                      │
│                              │   消息总线      │                                      │
│                              │ (Event Bus)     │                                      │
│                              └────────┬────────┘                                      │
└───────────────────────────────────────┼─────────────────────────────────────────────────┘
                                        │
        ┌───────────────────────────────┼───────────────────────────────┐
        │                               │                               │
        ▼                               ▼                               ▼
┌───────────────┐              ┌───────────────┐              ┌───────────────┐
│  实例节点 1   │              │  实例节点 2   │              │  实例节点 N   │
│  (Gateway)    │              │  (Gateway)    │              │  (Gateway)    │
├───────────────┤              ├───────────────┤              ├───────────────┤
│ License Agent │              │ License Agent │              │ License Agent │
│ Health Reporter│              │ Health Reporter│              │ Health Reporter│
│ Auto-Updater  │              │ Auto-Updater  │              │ Auto-Updater  │
└───────────────┘              └───────────────┘              └───────────────┘
```

---

## 二、故障自愈与运维平台

### 2.1 核心功能模块

#### A. 故障检测层
```
检测类型:
├── 路由错误检测      - 无可用候选节点、路由失败
├── 凭据故障检测      - 认证失败、配额耗尽、连接超时
├── 失败率监控        - 滑动窗口失败率超过阈值
├── 延迟异常检测      - P99延迟超过SLA
└── 资源耗尽检测      - 连接池满、内存压力
```

#### B. 故障自愈策略引擎

基于您的选择，实现三种修复策略：

**1. 自动重启故障组件**
```go
type RestartStrategy struct {
    MaxRetries      int           // 最大重试次数
    RetryInterval   time.Duration  // 重试间隔
    CoolDown        time.Duration // 重启后冷却期
}

// 检测到凭据连续失败 → 重启凭据连接池 → 验证恢复
func (s *RestartStrategy) Execute(ctx context.Context, problem Problem) error {
    // 1. 标记凭据为cooling状态
    // 2. 重置连接池
    // 3. 等待冷却期
    // 4. 探测验证
    // 5. 恢复或升级告警
}
```

**2. 自动切换备用资源**
```go
type FailoverStrategy struct {
    BackupCandidates []*Candidate  // 备用候选列表
    HealthThreshold  float64       // 健康度阈值
}

// 检测到主凭据故障 → 自动切换到备用凭据 → 更新路由
func (s *FailoverStrategy) Execute(ctx context.Context, problem Problem) error {
    // 1. 查询备用候选
    // 2. 健康检查
    // 3. 切换流量
    // 4. 监控新路径
    // 5. 旧路径恢复后通知
}
```

**3. 自动回滚配置**
```go
type RollbackStrategy struct {
    ConfigSnapshotTTL time.Duration // 配置快照保留时间
}

// 检测到配置变更后异常 → 回滚到上一个稳定配置
func (s *RollbackStrategy) Execute(ctx context.Context, problem Problem) error {
    // 1. 保存当前配置快照
    // 2. 加载上一个稳定版本
    // 3. 应用回滚
    // 4. 验证系统稳定
    // 5. 通知管理员
}
```

#### C. 日志聚合与分析

```
┌──────────────────────────────────────────────────────────────┐
│                      日志分析引擎                            │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  日志采集 ──▶ 格式标准化 ──▶ 异常检测 ──▶ 根因分析 ──▶ 修复  │
│     │                                      │                 │
│     ▼                                      ▼                 │
│  ┌─────────┐    ┌─────────────┐    ┌─────────────┐         │
│  │ Loki/   │    │ 异常模式    │    │ 修复建议    │         │
│  │ Elastic │───▶│ 匹配器      │───▶│ 生成器      │         │
│  └─────────┘    └─────────────┘    └─────────────┘         │
│                       │                                    │
│                       ▼                                    │
│                 ┌───────────┐                              │
│                 │ 摘要生成  │ ──▶ 回传到中心节点            │
│                 └───────────┘                              │
└──────────────────────────────────────────────────────────────┘
```

### 2.2 数据模型

```sql
-- 故障事件表
CREATE TABLE fault_events (
    id              BIGSERIAL PRIMARY KEY,
    instance_id     TEXT NOT NULL,
    fault_type      TEXT NOT NULL,  -- route_error, credential_failure, high_latency
    severity        TEXT NOT NULL,  -- critical, major, minor
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at     TIMESTAMPTZ,
    root_cause      TEXT,
    fix_applied     TEXT,
    status          TEXT NOT NULL DEFAULT 'detected',  -- detected, analyzing, fixing, resolved
    raw_logs        JSONB,          -- 原始日志摘要
    summary         TEXT,           -- AI生成的摘要
    metadata        JSONB
);

-- 修复历史表
CREATE TABLE fix_history (
    id              BIGSERIAL PRIMARY KEY,
    fault_id        BIGINT REFERENCES fault_events(id),
    strategy        TEXT NOT NULL,  -- restart, failover, rollback
    attempted_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    success         BOOLEAN,
    duration_ms     INT,
    details         JSONB
);

-- 实例状态表
CREATE TABLE gateway_instances (
    instance_id     TEXT PRIMARY KEY,
    hostname        TEXT,
    ip_address      TEXT,
    version         TEXT,
    status          TEXT NOT NULL,  -- healthy, degraded, down
    last_heartbeat  TIMESTAMPTZ,
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata        JSONB
);

-- 实例健康指标
CREATE TABLE instance_metrics (
    instance_id     TEXT NOT NULL,
    ts              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    cpu_usage       NUMERIC(5,2),
    memory_usage    NUMERIC(5,2),
    request_count   INT,
    error_count     INT,
    avg_latency_ms  INT,
    PRIMARY KEY (instance_id, ts)
);
```

### 2.3 中心节点通信协议

```go
// 上报数据结构
type InstanceReport struct {
    InstanceID     string            `json:"instance_id"`
    Version       string            `json:"version"`
    Timestamp     time.Time         `json:"timestamp"`
    HealthStatus  string            `json:"health_status"`
    Metrics       InstanceMetrics   `json:"metrics"`
    FaultEvents   []FaultEvent      `json:"fault_events,omitempty"`
    Logs          []LogChunk        `json:"logs,omitempty"`  // 摘要日志
}

// 下发命令
type CenterCommand struct {
    Type    string      `json:"type"`  // config_update, upgrade, force_restart
    Payload json.RawMessage `json:"payload"`
    At      time.Time   `json:"at,omitempty"`
}
```

---

## 三、自动升级系统

### 3.1 分阶段灰度发布

基于您的选择，采用分阶段灰度发布策略：

```
阶段1 (5%) ──▶ 阶段2 (20%) ──▶ 阶段3 (50%) ──▶ 阶段4 (100%)
   │              │               │               │
   ▼              ▼               ▼               ▼
内部测试      早期用户        多数用户        全量推送
```

### 3.2 版本管理

```go
type VersionInfo struct {
    Version       string    `json:"version"`        // 语义化版本: 2.5.0
    BuildHash     string    `json:"build_hash"`     // Git commit hash
    ReleaseDate   time.Time `json:"release_date"`
    MinVersion    string    `json:"min_version"`    // 最低支持版本
    RolloutPhase  int       `json:"rollout_phase"`  // 0-3
    Checksum      string    `json:"checksum"`       // SHA256
    Signature     string    `json:"signature"`      // RSA签名
    ReleaseNotes  string    `json:"release_notes"`
}

type RolloutConfig struct {
    Phase       int           `json:"phase"`
    Percentage  float64       `json:"percentage"`  // 0.0-1.0
    StartTime   time.Time     `json:"start_time"`
    MinVersion  string        `json:"min_version"` // 强制升级版本
}
```

### 3.3 升级流程

```go
// 客户端升级检查
func (u *AutoUpdater) CheckAndUpgrade(ctx context.Context) error {
    // 1. 检查当前版本
    current := version.Get()

    // 2. 查询中心节点版本信息
    latest, err := u.fetchLatestVersion(ctx)
    if err != nil {
        return err
    }

    // 3. 比较版本
    if !needsUpgrade(current, latest) {
        return nil
    }

    // 4. 检查灰度策略
    if !latest.rolloutConfig.IsMyTurn(u.instanceID) {
        slog.Info("not in rollout group yet", "phase", latest.rolloutConfig.Phase)
        return nil
    }

    // 5. 下载并验证
    if err := u.downloadAndVerify(ctx, latest); err != nil {
        return err
    }

    // 6. 执行升级
    return u.applyUpgrade(ctx, latest)
}

// 灰度策略判断
func (rc *RolloutConfig) IsMyTurn(instanceID string) bool {
    if rc.Phase == 0 {
        return true // 测试阶段
    }
    // 基于instanceID哈希分配百分比
    hash := crc32.ChecksumIEEE([]byte(instanceID))
    threshold := int(float64(math.MaxUint32) * rc.Percentage)
    return int(hash) < threshold
}
```

### 3.4 安全验证

```go
// 升级包验证
func VerifyUpgradePackage(data []byte, info *VersionInfo, publicKey *rsa.PublicKey) error {
    // 1. 校验Checksum
    hash := sha256.Sum256(data)
    if hex.EncodeToString(hash[:]) != info.Checksum {
        return errors.New("checksum mismatch")
    }

    // 2. 校验RSA签名
    sig, _ := base64.StdEncoding.DecodeString(info.Signature)
    err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, hash[:], sig)
    if err != nil {
        return errors.New("signature verification failed")
    }

    return nil
}
```

---

## 四、License管理系统

### 4.1 设备绑定策略

基于您的需求：**一个实例最多安装在2台设备上，激活需要销毁一个当前设备**

```
┌─────────────────────────────────────────────────────────────┐
│                    License设备管理                          │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│   实例 License (max_devices=2)                              │
│   ├── 设备A (激活中)  ──▶ HWID: abc123                    │
│   └── 设备B (激活中)  ──▶ HWID: def456                    │
│                                                              │
│   用户操作:                                                  │
│   1. 激活新设备C → 需要先销毁A或B                          │
│   2. 选择"替换设备" → B被标记为deactivated                │
│   3. C激活成功 → 状态更新到License服务器                   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### 4.2 硬件指纹生成

```go
type HardwareFingerprint struct {
    MachineID    string   // 机器唯一ID (os/share)
    CPUInfo      string   // CPU型号
    HostID       string   // 主机ID
    MACAddresses []string // MAC地址列表
    DiskSerial   string   // 磁盘序列号
}

func GenerateFingerprint() (*HardwareFingerprint, error) {
    fp := &HardwareFingerprint{}

    // Machine ID - 跨重启持久化
    id, err := machineid.ID()
    if err == nil {
        fp.MachineID = id
    }

    // CPU信息
    cpuInfo, _ := cpu.Info()
    if len(cpuInfo) > 0 {
        fp.CPUInfo = fmt.Sprintf("%s:%s", cpuInfo[0].ModelName, cpuInfo[0].PhysicalID)
    }

    // 主机ID
    hostInfo, _ := host.Info()
    if hostInfo != nil {
        fp.HostID = hostInfo.HostID
    }

    // MAC地址
    interfaces, _ := net.Interfaces()
    for _, iface := range interfaces {
        if len(iface.HardwareAddr) > 0 {
            fp.MACAddresses = append(fp.MACAddresses, iface.HardwareAddr.String())
        }
    }

    // 磁盘序列号
    diskInfo, _ := disk.Info()
    if diskInfo != nil {
        fp.DiskSerial = diskInfo.SerialNumber
    }

    return fp, nil
}

// 生成稳定设备码
func (fp *HardwareFingerprint) Hash() string {
    data := fmt.Sprintf("%s|%s|%s|%s",
        fp.MachineID, fp.CPUInfo, fp.HostID, fp.DiskSerial)
    hash := sha256.Sum256([]byte(data))
    return hex.EncodeToString(hash[:16]) // 取前16字节
}
```

### 4.3 激活流程 (在线+离线混合)

**在线激活流程:**
```
客户端                          License服务器
   │                                  │
   │──── 激活请求 ────────────────────▶│
   │    {license_key, hwid}           │
   │                                  │ 验证License Key
   │                                  │ 检查设备限制
   │                                  │ 查询已激活设备
   │                                  │
   │◀─── 响应 (选择设备) ─────────────│
   │    {deactivate_url, activate_token}│
   │                                  │
   │  用户选择设备X替换               │
   │──── 确认替换 ────────────────────▶│
   │                                  │ 更新设备列表
   │◀─── 激活成功 ───────────────────│
   │    {signed_license, expires_at}  │
   │                                  │
```

**离线激活流程:**
```
1. 客户端生成离线请求文件
   offline_request_{machine_hash}.txt

2. 用户上传到License服务器 (手动/邮件)

3. 管理员审核并生成离线响应
   offline_response_{request_id}.txt

4. 用户导入响应文件完成激活
```

### 4.4 防破解设计

**多重验证机制:**

```go
type LicenseValidator struct {
    publicKey     *rsa.PublicKey
    serverPubKey  string  // License服务器公钥 (防止伪造)
    clock         *Clock  // 可Mock用于测试
}

// 验证签名
func (v *LicenseValidator) Validate(signedData []byte) (*License, error) {
    // 1. 解析签名数据
    var signed SignedLicense
    if err := json.Unmarshal(signedData, &signed); err != nil {
        return nil, err
    }

    // 2. 验证签名 (RSA)
    hash := sha256.Sum256(signed.Data)
    if err := rsa.VerifyPKCS1v15(v.publicKey, crypto.SHA256, hash[:], signed.Signature); err != nil {
        return nil, errors.New("invalid signature")
    }

    // 3. 反序列化License
    var lic License
    if err := json.Unmarshal(signed.Data, &lic); err != nil {
        return nil, err
    }

    // 4. 验证过期时间
    if v.clock.Now().After(lic.ExpiresAt) {
        return nil, errors.New("license expired")
    }

    // 5. 验证硬件指纹
    if !v.validateHardwareBinding(lic) {
        return nil, errors.New("hardware mismatch")
    }

    return &lic, nil
}

// 时间篡改检测
func (v *LicenseValidator) detectTimeTampering() bool {
    // 1. NTP时间校验
    ntpTime, err := getNTPTime("pool.ntp.org")
    if err == nil {
        diff := time.Since(ntpTime)
        if diff > 5*time.Minute || diff < -5*time.Minute {
            return true // 时间被篡改
        }
    }

    // 2. 单调时钟检测
    lastCheck := loadLastCheckTime()
    monotonic := time.Since(lastCheck)
    // 如果单调时钟和实时时间差异过大，说明时间被回拨
    return false
}
```

**代码混淆:**
- 使用 `garble` 工具混淆Go二进制
- 关键License验证逻辑使用动态函数地址调用
- 公钥加密存储，运行时解密

### 4.5 数据模型

```sql
-- License定义
CREATE TABLE licenses (
    id                  BIGSERIAL PRIMARY KEY,
    license_key         TEXT NOT NULL UNIQUE,
    customer_name       TEXT NOT NULL,
    customer_email      TEXT NOT NULL,
    max_devices         INT NOT NULL DEFAULT 2,
    features            JSONB,              -- 功能模块列表
    expires_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at          TIMESTAMPTZ,
    notes               TEXT
);

-- 设备激活记录
CREATE TABLE license_devices (
    id                  BIGSERIAL PRIMARY KEY,
    license_id          BIGINT REFERENCES licenses(id),
    instance_id         TEXT NOT NULL,
    hardware_hash       TEXT NOT NULL,
    device_name         TEXT,
    activated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_heartbeat_at   TIMESTAMPTZ,
    status              TEXT NOT NULL DEFAULT 'active',  -- active, deactivated, suspended
    deactivated_at      TIMESTAMPTZ,
    deactivate_reason   TEXT,
    metadata            JSONB
);

-- 激活请求日志 (审计)
CREATE TABLE license_activations (
    id                  BIGSERIAL PRIMARY KEY,
    license_id          BIGINT REFERENCES licenses(id),
    device_id           BIGINT REFERENCES license_devices(id),
    request_type        TEXT NOT NULL,  -- online_activate, offline_activate, deactivate
    request_data        JSONB,
    response_data       JSONB,
    ip_address          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status              TEXT NOT NULL   -- pending, approved, rejected
);

-- 离线激活请求
CREATE TABLE offline_activation_requests (
    id                  BIGSERIAL PRIMARY KEY,
    license_id          BIGINT REFERENCES licenses(id),
    request_file        TEXT NOT NULL,
    request_data        JSONB,  -- {machine_hash, machine_info}
    status              TEXT NOT NULL DEFAULT 'pending',  -- pending, approved, rejected
    reviewed_by         TEXT,
    reviewed_at         TIMESTAMPTZ,
    response_file       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

---

## 五、核心网关功能扩展

### 5.1 产品下载与License申请

在网关管理界面增加:

```
┌─────────────────────────────────────────────────────────────┐
│  网关管理界面                                               │
├─────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐        │
│  │ 产品下载    │  │ License申请  │  │ 设备管理    │        │
│  │             │  │             │  │             │        │
│  │ 最新版本:   │  │ 申请新License│  │ 设备列表:   │        │
│  │ v2.4.2      │  │ 激活设备    │  │ - 设备A ✓   │        │
│  │             │  │             │  │ - 设备B ✓   │        │
│  │ [下载]      │  │ [申请]      │  │ [替换]      │        │
│  └─────────────┘  └─────────────┘  └─────────────┘        │
└─────────────────────────────────────────────────────────────┘
```

### 5.2 管理员视图 - 用户与License总览

```
┌─────────────────────────────────────────────────────────────┐
│  中心管理平台 - 超级管理员视图                               │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ 用户管理                                              │  │
│  │ ├─ 用户总数: 156                                     │  │
│  │ ├─ 活跃用户: 142                                     │  │
│  │ └─ 本月新增: 12                                     │  │
│  │                                                      │  │
│  │ 用户列表:                                             │  │
│  │ ┌────────┬────────┬────────┬────────┬────────┐       │  │
│  │ │用户名  │Email   │实例数  │License │状态    │       │  │
│  │ ├────────┼────────┼────────┼────────┼────────┤       │  │
│  │ │张三    │a@b.com │2/2     │有效    │活跃    │       │  │
│  │ │李四    │c@d.com │1/2     │有效    │活跃    │       │  │
│  │ └────────┴────────┴────────┴────────┴────────┘       │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ License概览                                           │  │
│  │ ├─ License总数: 89                                    │  │
│  │ ├─ 激活设备: 167                                     │  │
│  │ ├─ 即将过期(<30天): 5                                │  │
│  │ └─ 已过期: 2                                         │  │
│  │                                                      │  │
│  │ 安装统计:                                             │  │
│  │ - v2.4.2: 45%                                        │  │
│  │ - v2.4.1: 35%                                        │  │
│  │ - v2.4.0: 15%                                        │  │
│  │ - older: 5%                                          │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ 实例运行情况                                          │  │
│  │ 健康: 78 │ 降级: 12 │ 离线: 3                       │  │
│  └──────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

---

## 六、实施计划

### Phase 1: 基础设施 (2周)
1. 数据库表结构设计与迁移
2. License核心算法实现 (指纹、加密、验证)
3. 消息总线基础设施

### Phase 2: 核心功能 (3周)
1. License激活/停用流程
2. 故障检测与自愈引擎
3. 自动升级系统基础
4. 实例注册与心跳

### Phase 3: 管理界面 (2周)
1. 管理员Dashboard
2. 用户/License管理界面
3. 产品下载功能
4. 设备激活管理

### Phase 4: 高级功能 (2周)
1. 日志聚合与分析
2. AI驱动的故障摘要生成
3. 远程配置下发
4. 灰度发布优化

### 总工期: 9周

---

## 七、技术选型总结

| 功能 | 技术方案 | 理由 |
|------|---------|------|
| 故障自愈 | Operator模式 + 策略引擎 | 与K8s设计理念一致，易于扩展 |
| 日志收集 | Loki/Promtail | 轻量级，与Prometheus生态集成 |
| 自动升级 | 分阶段灰度 + RSA签名 | 风险可控，安全验证 |
| License | RSA签名 + AES加密 + 硬件指纹 | 防篡改，多重保护 |
| 设备绑定 | 2设备限制 + 用户选择替换 | 满足需求，灵活性高 |
| 通信协议 | WebSocket + JSON | 实时性好，易于调试 |

---

## 八、风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| License被破解 | 高 | 代码混淆 + 定期更新验证算法 |
| 升级失败导致服务中断 | 高 | 灰度发布 + 自动回滚机制 |
| 网络分区导致激活失败 | 中 | 离线激活备选方案 |
| 硬件变更导致指纹变化 | 中 | 允许有限度的指纹漂移 |
| 中心节点单点故障 | 高 | 多中心部署 + 本地缓存 |
