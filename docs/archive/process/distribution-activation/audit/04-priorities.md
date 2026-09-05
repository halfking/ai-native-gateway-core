# 优先级矩阵 + 工作量估算 · 2026-07-14

> 14 个 gap → P0/P1/P2 三档 + 工作量估算 + Agent 分配。
> 工作量基于"独立可交付"原则估算（包含代码 + 测试 + 文档）。

## 1. 优先级矩阵

| 优先级 | 含义 | 时间窗 |
|--------|------|--------|
| **P0** | 阻塞客户主路径；试用转化受损；监控盲区；安全延展 | 12 周 |
| **P1** | 重要扩展能力；远程协助 / 灰度发布；高级防盗版 | 12-24 周 |
| **P2** | 优化项 / 小众场景 / 安全 hardening | 24+ 周 |

## 2. P0（6 项 · 12 周内必完）

| ID | 项 | 工作量 | Owner Agent | 依赖 | 验收 |
|----|---|--------|-------------|------|------|
| **G-C1** | 浏览器侧 License API（trial/activate/offline/status）| **3d** | D (Client-System-API) | G-U3 命令通道可后置 | e2e：浏览器点击激活 → 收到 license.dat |
| **G-C2** | 升级 Banner + UpgradePanel UI | **5d** | Y (Client-FE) | M2-T13/T14 后端 | e2e：首页看到 Banner / 设置页看到进度 |
| **G-K1** | 采集器代码（collector/metrics/reporter）| **5d** | D (Client-System-API) | G-S2 接收端 | 5min 上报 → 主控端存储 → 仪表盘显示 |
| **G-S2** | 采集数据接收端 `/api/v1/collect/runtime` | **3d** | C (Authority-Service) | G-K1 schema | 同上 |
| **G-U1** | 主控端 push 通道（WebSocket + SSE 双模）| **8d** | C + D + X | G-U3 + G-S1 | e2e：主控推"升级到 v2.1"命令 → 30s 内客户端执行 |
| **G-U3** | 客户端 `/api/center/command` 接收器 + 危险命令审批 | **5d** | B + D | G-U1 | e2e：命令入列 → 客户确认 → 执行 → 回执 |

**P0 总工作量：29 人日 ≈ 6 周（10 Agent 并行）/ 12 周（4 Agent 串行）**

## 3. P1（8 项 · 12-24 周）

| ID | 项 | 工作量 | Owner Agent |
|----|---|--------|-------------|
| **G-C3** | 独立 `kx-gateway-rollback` CLI | 1d | E (Upgrader) |
| **G-C4** | Telemetry 设置 UI | 3d | Y |
| **G-C5** | License 详情 UI | 2d | Y |
| **G-S1** | 远程协助命令工作流（SSH reverse / 命令面板）| 5d | C + X |
| **G-S3** | 告警系统（alerts 表 + 规则引擎 + Webhook）| 4d | C |
| **G-S6** | 灰度发布规则引擎（5 阶段）| 3d | C + X |
| **G-K2** | 二进制自校验 `antitamper.go` | 3d | B (Client-Go-Core) |
| **G-K5** | 增强指纹 `enhanced_fingerprint.go` | 2d | B |

**P1 总工作量：23 人日 ≈ 5 周并行**

## 4. P2（6 项 · 24+ 周）

| ID | 项 | 工作量 | Owner Agent |
|----|---|--------|-------------|
| **G-C6** | 浏览器侧离线激活 UI | 2d | Y |
| **G-S4** | 下载请求埋点 + 统计 | 2d | O + X |
| **G-S5** | 多版本发布 UI（上传/发布/灰度 3 步）| 3d | X |
| **G-K3** | 反调试 `antidebug.go` | 1d | B |
| **G-K4** | nonce 防重放 `nonce.go` | 1d | B |
| **G-U2** | 危险命令双人复核 + OTP 工作流 | 4d | C + X |

**P2 总工作量：13 人日 ≈ 3 周并行**

## 5. 依赖图

```
Phase 0 (week 1-2): G-C1 + G-U3 + G-S2 (基础设施)
  ↓
Phase 1 (week 3-5): G-K1 (依赖 G-S2 schema) + G-U1 (依赖 G-U3)
  ↓
Phase 2 (week 6-8): G-C2 (依赖 G-U1 升级命令通道) + G-C4 (依赖 G-K1)
  ↓
Phase 3 (week 9-10): P1 全部
  ↓
Phase 4 (week 11-12): 集成 + GA
```

## 6. 风险登记表

| 风险 | 影响 | 缓解 |
|------|------|------|
| G-U1 通道延迟 > 30s | 紧急回退失效 | 客户端常驻 WebSocket + 5s 心跳 |
| 客户端网络抖动 / 离线 | push 命令丢失 | 客户端轮询补偿 + 命令 TTL 7 天 |
| G-S6 灰度规则出错 | 部分客户错失补丁 | 默认全量 + 灰度手动开关 |
| G-K2 自校验 false-positive | 客户被拒绝启动 | SHA256 与版本 SSOT 同步，break-glass 启动参数 |
| 主控账号被盗 | 全网停摆 | G-U2 危险命令双人复核 + OTP |

## 7. ROI 估算（用户价值）

| 项 | 月增试用数（估算） | 商用转化提升 | 防盗版保护 |
|----|-------------------|-------------|-----------|
| G-C1 浏览器激活 | +30% | +10% 试用→付费 | - |
| G-C2 升级 Banner | - | - | 安全补丁覆盖 +25% |
| G-U1 升级推送 | - | - | 紧急回退从 6h → 30s（180 倍提速）|
| G-K2/K5 防盗版 | - | - | 堵住 KVM 克隆 + 二进制 patch |

## 8. 与 docs/分发与激活/11-实施路线图.md 对照

| 旧里程碑 | 新增 / 升级 |
|----------|------------|
| M1-C8 激活向导 UI | = G-C1（落地）|
| M2-C7 设置页升级面板 | = G-C2 |
| M3-C1-C4 采集与上报 | = G-K1 + G-S2 |
| M3-C7-C10 防盗版 | = G-K2/K3/K4/K5 |
| M2-M2 灰度规则 | = G-S6 |
| M2-M3 回退下发 | = G-U1 + G-U3 |
| **新增** | G-S1 远程协助 / G-S3 告警 / G-U2 命令审批 |

## 9. 验收口径

12 周内 P0 完成需达成：

- [ ] `http://server:8781/setup` 在浏览器中点 3 下完成试用激活（不需 SSH）
- [ ] 首页出现升级 Banner；点击 → UpgradePanel 展示进度
- [ ] 客户端 5min 上报 runtime metrics → 主控端存储 → 仪表盘显示
- [ ] 主控端推"升级"命令 → 30s 内客户端启动升级 + 进度回写
- [ ] 客户端离线命令队列：7 天内 rejoin 后自动执行
- [ ] 危险命令（rollback / wipe）需要 OTP 二次确认