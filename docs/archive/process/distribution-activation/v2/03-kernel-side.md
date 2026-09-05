# 内核侧增强 v2 · 2026-07-14

> 内核侧是客户节点的信任边界：指纹、License、时间、采集、命令执行和升级回滚都在此收敛。

## 1. 模块边界

```text
licensing/
  crypto.go              签名/加密/验证
  fingerprint.go         基础指纹
  enhanced_fingerprint.go 增强指纹与漂移策略
  clock.go               时间单调性与服务端时间
  enforcement.go         启动/运行/关键操作授权
  antitamper.go          发行物完整性校验
  antidebug.go           运行环境风险信号
  nonce.go               请求和离线激活防重放
  device_manager.go      设备绑定与迁移
  grace.go               宽限期策略

internal/collector/
  collector.go           周期编排和本地授权判断
  metrics.go             白名单指标计算
  reporter.go            批量、重试、退避、删除请求

internal/command/
  receiver.go            命令接收、签名、幂等、过期
  policy.go              命令权限与客户确认
  executor.go            白名单命令执行
```

## 2. License 计算与验证

License 的有效性由多个独立条件组成，不允许一个布尔变量覆盖所有错误：

```text
signature_valid
  AND not_revoked
  AND now < expires_at
  AND fingerprint_score >= threshold
  AND clock_valid
  AND feature_entitled
  AND device_count <= max_devices
```

输出结构必须区分：`valid`、`grace`、`restricted`、`revoked`、`tampered`、`clock_rollback`、`fingerprint_mismatch`。这样中心预警和客户 UI 能准确说明原因。

## 3. 设备端信息获取

### 3.1 稳定字段

- machine-id、CPU 型号、HostID、主 MAC。
- disk serial、BIOS/product UUID、虚拟化类型、云厂商。
- OS、arch、container runtime、安装版本、instance_id。

### 3.2 易变字段

- boot_id、uptime、进程数、容器 ID。

易变字段只用于诊断和异常判断，不参与硬绑定。稳定字段先规范化再哈希，禁止将原始 MAC、序列号和 hostname 上报中心。

### 3.3 指纹漂移策略

```text
score >= 0.75       正常
0.60 <= score < .75 记录 drift，允许运行，要求下次在线确认
score < 0.60        restricted，进入重新激活流程
```

中心允许管理员对一次硬件迁移发放短期迁移票据，票据一次性、签名且有过期时间，禁止直接修改客户数据库绕过绑定。

## 4. 使用时长管理

本地 License 到期、中心不可达和心跳失败必须分开处理：

| 状态 | 行为 |
|------|------|
| 正常 | 全功能，按策略心跳和 refresh |
| Grace | 保留业务，显示预警，限制高风险管理动作 |
| Restricted | 只读/基础路由，保留激活和导出能力 |
| Revoked | 停止受保护能力，允许查看原因和离线申诉入口 |

ClockGuard 持久化最近启动时间、最近验证时间和服务端时间样本；检测到回拨时不得静默修正系统时间，只记录审计并进入 restricted。

## 5. 采集 API 与数据管理

### 5.1 白名单指标

只允许系统资源、聚合流量、版本、License 状态和错误分类。绝不读取 prompt、completion、文件内容、API Key 明文或完整网络身份。

### 5.2 本地队列

采集失败不影响网关业务。使用带上限的本地队列：

- 最大 1000 条或 10MB。
- 指数退避，最大重试 24h。
- 队列满时丢弃最旧的非关键指标。
- opt-out 后立即停止新采集，并生成删除请求。

### 5.3 中心数据保留

| 数据 | 保留期 | 说明 |
|------|-------|------|
| 节点当前状态 | 90 天 | 用于运营分析 |
| 5min 聚合指标 | 30 天 | 超期降采样 |
| 日聚合指标 | 2 年 | 容量和版本趋势 |
| 原始下载事件 | 90 天 | 仅匿名 request_id |
| 命令审计 | 2 年 | 不可篡改追加写 |

## 6. 防盗版增强

### P1

- 完整性校验绑定 release manifest 的 SHA256，不能硬编码一个跨版本 hash。
- 增强指纹和迁移票据。
- 请求 nonce、时间戳、instance_id 绑定，服务端 Redis 去重。

### P2

- Linux ptrace/LD_PRELOAD 风险信号，不以单一信号直接误杀。
- 构建流水线中对 customer 产物做签名和可追溯 attestations。
- 私钥进入 KMS/HSM；签发操作需要 operator + approval_id。

反调试和反篡改不能作为唯一安全边界。真正边界仍是服务端签名、设备绑定、吊销和可追溯发布。

## 7. 内核侧验收

- 修改 License 任意字段都被拒绝。
- 复制到另一设备进入 fingerprint_mismatch。
- 时钟回拨进入 clock_rollback，恢复正确时间后可按策略恢复。
- 采集 opt-out 后不再产生新上报，删除请求最终可查。
- 重复 command_id 不重复执行。
- 过期、错误签名、错误 instance_id 的命令都拒绝。
