# 48 小时变更审计报告（2026-07-11 ~ 2026-07-13）

> 分析范围：`f3ffd516ec0` ~ `80267ce03`（HEAD），共 291 个 commits
> 作者：opencode-agent (227) / halfking (59) / opencode-ai-agent (5)
> 变更文件：4167 个（+1,135,606 / -24,651 行）

---

## 一、功能分类及重要性评估

### [P0] 关键系统功能

| 分类 | Commits | 重要性 | 说明 |
|------|---------|--------|------|
| **License 授权系统** (W1-W5) | 40+ | ★★★★★ | 新增 `cmd/license-authority` 全流程：在线/离线激活、JWT 签发、心跳、升级 |
| **路由可靠性** | 15+ | ★★★★★ | sticky 路由审计 (audit-2/3)，候选去重，false tool error 修复 |
| **部署架构迁移** | 20+ | ★★★★★ | 71/184 退役 → 154/252/245/kaixuan-1/2/3，target-aware 部署 |

### [P1] 重要修复

| 分类 | Commits | 重要性 | 说明 |
|------|---------|--------|------|
| **安全审计修复** | 15+ | ★★★★☆ | P0/P1 凭据硬编码清理、注入检测、SSE JWT leak、DingTalk/Feishu 加固 |
| **计费系统** | 12+ | ★★★★☆ | plan_type SSOT、usage view 降级、消费审计详情、账单历史 |
| **数据生命周期** | 10+ | ★★★★☆ | 降级恢复控制、热表管理、hot migration、TTL、分区审计 |

### [P2] 运营与前端

| 分类 | Commits | 重要性 | 说明 |
|------|---------|--------|------|
| **运维平台前端** | 20+ | ★★★☆☆ | ops sub-pages（Center/Fault/AutoUpdate/License/VibeCoding） |
| **i18n 补全** | 10+ | ★★★☆☆ | 47 个 locale 文件去重，ops 模块 8 语种同步 |
| **Session Forensics** | 8+ | ★★★☆☆ | 会话调试模块 + 场景注入测试 |
| **前端 UI 修复** | 15+ | ★★★☆☆ | 暗色主题、ops 路由、压缩面板重构、SessionConfig 视图 |

### [P3] 辅助功能

| 分类 | Commits | 重要性 | 说明 |
|------|---------|--------|------|
| **自检功能** | 4+ | ★★☆☆☆ | 系统自检模块（SelfCheckPanel + API） |
| **测试脚本** | 8+ | ★★☆☆☆ | mock_supplier 16 场景重构、数据库双向同步 |
| **文档** | 20+ | ★★☆☆☆ | 设计规格、审计报告、changelog |
| **版本号提升** | 5 | ★☆☆☆☆ | build_seq 971→975 |

---

## 二、业务逻辑审计 — 代码 vs 方案

### 2.1 License & Distribution（docs/superpowers/specs/2026-07-12-fast-deploy-design.md）

#### 已对齐（✅）

| 需求 | 状态 | 代码位置 |
|------|------|----------|
| `cmd/license-authority` 入口 + `:8443` 监听 | ✅ | `cmd/license-authority/main.go` |
| `verify_local.go` RSA 验签 + 过期/撤销/指纹 | ✅ | `licensing/verify_local.go` |
| `clock.go` 时钟回拨检测 | ✅ | `licensing/clock.go` |
| EdDSA JWT 7 天 TTL | ✅ | `cmd/license-authority/jwt_eddsa.go` |
| Redis nonce 缓存 (5min SETNX) | ✅ | `cmd/license-authority/middleware/redis_nonce.go` |
| refresh_token 90 天 + rotation | ✅ | `cmd/license-authority/refresh_handler.go`, `refresh_token.go` |
| 376 迁移 (gateway_instances auth) | ✅ | `sql/migrations/startup/up/376_gateway_instances_auth.sql` |
| 377 迁移 (heartbeats 分区) | ✅ | `sql/migrations/startup/up/377_instance_heartbeats_partition.sql` |
| 注册/心跳/升级 全部 handler | ✅ | `cmd/license-authority/` 下 15+ 文件 |

#### 关键偏离（⚠️）

| 偏离项 | 严重度 | 详情 |
|--------|--------|------|
| **`state.go` 缺失** — 升级状态机未形式化 | **高** | 方案要求 `installer/internal/upgrader/state.go` 枚举 `pending/downloading/ready_to_restart/upgrading/success`，不存在。状态机隐含在 `apply.go` 内联逻辑中 |
| **`keys.go` 缺失** — 客户端 keypair 管理 | **中** | 方案要求 `installer/internal/enrollment/keys.go`，不存在。客户端 Ed25519 keypair 管理无独立模块 |
| **Report HTTP stubs** — 升级结果未实际上报 | **中** | `installer/internal/upgrader/client.go:83-88,100-105` 的 `ReportUpdate()`/`ReportRollback()` 为空实现，注释 `// 暂不实现实际上报逻辑` |
| **`last_heartbeat_at`/`last_status` 缺失** | **低** | 方案 §4 line 288-289 要求这 2 列在 376 迁移中，实际 SQL 中未包含 |
| **COBRA 未使用** | **低** | 方案要求 Cobra 入口，使用 `flag` 替代 |
| **README.md 缺失** | **低** | 方案 8.1 列出 `cmd/license-authority/README.md`，不存在 |

---

### 2.2 计费模式标准化（docs/billing-mode-standardization.md + ADR-001）

#### 已对齐（✅）

| 需求 | 状态 | 代码位置 |
|------|------|----------|
| `DeriveBillingMode` 函数 | ✅ | `modelcatalog/upsert.go:34-39` |
| 数据修正脚本 (token→per_token) | ✅ | 迁移 327 |
| 路由兼容性通过 v_routable_credential_models 视图 | ✅ | `provider/client.go:795-797` |

#### 关键偏离（⚠️）

| 偏离项 | 严重度 | 详情 |
|--------|--------|------|
| **方案文档已过时** — 未反映迁移 335 的逆转 | **高** | 原方案假设 plan_type→billing_mode 派生关系，生产验证发现两者是采-销独立概念。迁移 335 已移除兼容性检查，但方案文档未更新 |
| **8 处 COALESCE 默认值仍用 `'token'`** | **中** | `provider/client.go:774,868`, `admin/routing.go:218,244,1839,1854,2558`, `admin/pricing.go:361` — 应改为 `'per_token'` |
| **健康检查误报风险** | **中** | `bg/routing_health_checks.go:21` 仍将 plan_type≠billing_mode 视为 critical 异常，迁移 335 后为合法配置 |

---

### 2.3 路由可靠性（multi-round audit）

#### 已对齐（✅）

| 修复 | 状态 | 说明 |
|------|------|------|
| audit sticky reroute + rate-limit gate | ✅ | `59cde3fdc` |
| sticky failure recording + multi-level cleanup | ✅ | `2909c7e82` |
| stale sticky cleanup + skip lookup when off | ✅ | `309f611f2` |
| duplicate candidate suppression | ✅ | `3ebbe2d63 / de3265ba` |
| 'No available accounts' → KindConcurrent | ✅ | `1f3eb5fad` |
| claude-opus-4-8 路由修复 | ✅ | `5ab27738` |
| 错误分类 P0 修复 (client vs upstream) | ✅ | `a90a984cc` |

#### 关键偏离

无严重偏离。路由修复已完成多轮审计，状态良好。

---

### 2.4 安全审计修复

#### 已对齐（✅）

| 修复 | 状态 | 说明 |
|------|------|------|
| 凭据硬编码 P0 清理 | ✅ | deploy 脚本占位符替换、clean HEAD |
| 指纹验证修复 (ConstantTimeCompare) | ✅ | `verify_local.go` 先哈希匹配再 fuzzy fallback |
| register_handler license_key fallback | ✅ | 通过 hardware_hash 找 license |
| 心跳签名占位符修复 | ✅ | HMAC-SHA256 + X-Timestamp + X-Nonce |
| SSE JWT leak 修复 | ✅ | URL parameter → Header |
| Feishu 回调加固 | ✅ | Unix-second 时间戳 + constant-time 比较 |
| DingTalk 加固 | ✅ | 安全边界 |
| 增强注入检测系统 | ✅ | 1500 万次压测验证 |

#### 关键偏离

无严重偏离。安全审计是 48h 内完成度最高的工作之一。

---

### 2.5 运维平台 Frontend

#### 已对齐（✅）

| 页面 | 状态 | 说明 |
|------|------|------|
| CenterOpsView | ✅ | 运维中心子页面 |
| FaultManagementView | ✅ | 故障管理 |
| AutoUpdateView | ✅ | 自动升级 |
| LicenseManagementView | ✅ | License 管理 |
| VibeCodingView | ✅ | VibeCoding 对齐 |
| SelfCheckPanel | ✅ | 系统自检 |
| SessionReplayView | ✅ | 会话重放 |
| TenantLicenseView / TenantAutoUpdateView | ✅ | 租户只读视图 |
| DegradationRecovery | ✅ | 数据降级恢复 |
| PromptInjectionConfigPanel | ✅ | 注入检测配置面板 |

#### 关键偏离

无严重偏离。前端实现完整，i18n 已完成 8 语言同步。

---

## 三、综合遗漏问题汇总

### 3.1 P0（必须修复）

| # | 问题 | 分类 | 位置 | 建议 |
|---|------|------|------|------|
| 1 | `installer/internal/upgrader/client.go`的`ReportUpdate`/`ReportRollback`为空实现 | License | 升级流程缺上报 | 补齐 HTTP 调用，否则主控端永远看不到升级结果 |
| 2 | `docs/billing-mode-standardization.md`方案文档严重过时 | 计费 | 文档 | 加入迁移 335 说明和采-销分离模型，标注 deprecated |

### 3.2 P1（建议修复）

| # | 问题 | 分类 | 位置 | 建议 |
|---|------|------|------|------|
| 3 | 8 处 COALESCE 默认值仍用 `'token'` | 计费 | `provider/client.go`, `admin/routing.go`, `admin/pricing.go` | 统一改为 `'per_token'` |
| 4 | `bg/routing_health_checks.go` 健康检查误报 critical | 计费 | 迁移 335 后合法配置被标记异常 | 降级为 warning 或移除 |
| 5 | 升级状态机未形式化 (`state.go` 缺失) | License | `installer/internal/upgrader/` | 建议补充，便于状态追踪和调试 |

### 3.3 P2（跟踪改进）

| # | 问题 | 分类 | 位置 | 建议 |
|---|------|------|------|------|
| 6 | `376_gateway_instances_auth.sql` 缺少 `last_heartbeat_at`/`last_status` 列 | License | 迁移文件 | 按方案 §4 补充 |
| 7 | `installer/internal/enrollment/keys.go` 缺失 | License | 客户端 keypair | 与 spec 对齐 |
| 8 | `cmd/license-authority` 使用 `flag` 而非 Cobra | License | 入口 | 技术债，与已有代码风格不一致 |

### 3.4 架构层面观察

| 观察 | 说明 |
|------|------|
| License 代码分布与方案偏差 | 方案规划逻辑放 `licensing/`/`center/`/`autoupdate/` 包，实际放 `cmd/license-authority/`，架构合理但应更新方案文档 |
| 计费 SSOT 方案设计方向有误 | 原方案假设 plan_type 派生 billing_mode，生产验证发现两者独立。已通过迁移 335 回滚，但文档未同步 |
| 路由审计完成度高 | 3 轮审计 + 独立修复，覆盖全面 |
| 安全修复范围广 | 从凭据硬编码到注入检测、回调签名、JWT 泄漏，覆盖面完整 |

---

## 四、阶段性结论

**48 小时内完成 291 个 commits，覆盖 11 个业务域，整体质量良好。**

- ✅ **Licensing**: 10/10 需求已实现，但有 1 个 P0 缺失（升级上报 stub）和 2 个 P1 偏离
- ✅ **计费**: 核心函数正确，但有方案过时和 8 处 COALESCE 默认值问题
- ✅ **路由**: 3 轮审计修复，质量高
- ✅ **安全**: 覆盖面最完整，无严重偏离
- ✅ **运维平台**: 前端对齐完整，i18n 同步到位

**紧急度排序：** `升级上报 stub (P0)` > `计费方案文档更新 (P0)` > `COALESCE 默认值 (P1)` > `健康检查误报 (P1)` > `state.go 补充 (P1)`
