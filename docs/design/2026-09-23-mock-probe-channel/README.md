# mock 探测通道（Self-Probe Channel）— 设计文档索引

> ## ⚠️ 状态更新（2026-09-24）：本文主体是 **v1 旧方案，已被取代**
>
> 1. **v1 已被 [03-optimization-plan.md](03-optimization-plan.md)（方案 v2）取代**。安全模型与可见性矩阵一律以 v2 + [05-implementation.md](05-implementation.md)（实施记录）为准；下述 v1 章节（含开头"草案待拍板"状态行、IsSelfProbe 字段、独立 prometheus.Registry、`__self_probe__` deny、127.0.0.1:auto 9-mode、FR-1 热重载等表述）**仅保留历史，勿按其实施**。
> 2. **落地实况速览（v2 实际形态，与 v1 章节的关键偏差）**：
>
>    | 维度 | v1 旧方案（本文下述章节） | v2 实际落地（03-plan §/05-implementation） |
>    |---|---|---|
>    | mock 供应商 | embedded.MockUpstream A/B（127.0.0.1:auto 独立监听） | 进程内 HTTP handler（`internal/providers/mock`：mock-fast / mock-slow），不占端口、不注册 providers 表 |
>    | 通道规模 | 2 providers × 2 models × 2 clients（9-mode 矩阵） | **2×2 = 4 通道**（mock-fast/mock-slow × stream/nonstream）；探测协议锁定 OpenAI Chat Completions，**无 9-mode** |
>    | 指标 | 独立 prometheus.Registry | **进业务 DefaultRegisterer**，/metrics 零改动暴露——v2 有意决策，非事故 |
>    | 业务隔离 | router `IsSelfProbe` 字段 + `__self_probe__` API key deny | Bearer `mock-probe-client` 系统白名单旁路（`internal/auth`）+ 关闭态"五不"硬隔离 |
>    | 热重载 | FR-1 开关热重载 | **无热重载**：`LLM_GATEWAY_MOCK_PROBE_ENABLED` 变更需重启进程 |
>    | Admin 可见性 | self_probe 标签过滤 | 默认仅隐藏 mock-fast/mock-slow（`MockProbeHideInAdmin`），其它 mock-x 不受影响 |
>
> 3. **入口现状（如实声明）**：当前装配点为 **`cmd/gateway-v2`（并行演示入口）**；生产主二进制 **`cmd/gateway` 未装配** mock probe 子系统——生产可达是**待拍板项**，拍板前该能力在生产环境不可达。

> 设计目录：`docs/design/2026-09-23-mock-probe-channel/`
> 状态：草案，待用户拍板后按 PR1→PR5 顺序落地
> 范围：网关主二进制 `cmd/gateway` 同进程内嵌；与外部 mock（python / docker-compose）做合并清理

---

## 0. 一句话目标

> 给网关增加一个 **同进程 Go 实现** 的 mock 2×2 自探测通道（2 个 mock 供应商 × 2 个 mock 客户端 → 4 条交叉路径），由单一开关 `MOCK_PROBE_ENABLED` 控制；关闭后 mock 客户端 **完全静默**、mock 供应商 **完全不显示**，对生产业务零影响。

---

## 1. 文档清单

| 文档 | 主题 | 状态 |
|------|------|------|
| [01-requirements.md](01-requirements.md) | 完整需求（FR/NFR + 9 mock mode 矩阵 + 边界与异常） | ✅ 已写 |
| [02-code-audit.md](02-code-audit.md) | 现有代码现状审计（涉及 router/registry/admin/scripts/mocks + 5 个重名坑 + python mock 现状） | ✅ 已写 |
| [03-optimization.md](03-optimization.md) | 架构与优化方案（embedded 子系统 + 4 条关键约束 + §11 防翻车附录） | ✅ 已写 |
| [03-optimization-plan.md](03-optimization-plan.md) | **方案 v2（取代本文 v1 章节）**：2×2 通道、协议锁定 chat/completions、指标进业务 registry、无热重载；安全模型与可见性矩阵以此为准 | ✅ 已定稿并落地 |
| [04-action-plan.md](04-action-plan.md) | 5 个 PR 的具体动作清单（开关位 / 注入位 / 验收点 + 跨阶段合并顺序） | ✅ 已写 |
| [05-implementation.md](05-implementation.md) | **实施记录（v2）**：落地清单、与方案偏差、验证记录、运维手册 | ✅ 已落地（2026-09-24） |

## 5. PR 合并顺序（来自 [04-action-plan §7](04-action-plan.md)）

```
PR1（骨架 + config + 启动顺序）
   ↓
PR2（MockUpstream 9 mode + 业务过滤）  ←  与 PR3 可并行；PR3 依赖 PR2 的 MP 注册
PR3（MockClient ticker + 静默闸 + 热重载）
   ↓
PR4（删 python mock，统一部署验证）
   ↓
PR5（测试 + 文档收口）
```

每个 PR 都独立可回滚（PR1 开关位就绪后才合入后续），全部合并需 ~4.5d。

---

## 2. 一张图（决策一览）

```
用户原话                                  我们的回答
─────────────────────────────────────────────────────────────────
2 个 mock 供应商同步启动                  → embedded.MockUpstream A/B（127.0.0.1:auto，9 种 mode）
定时通过 mock 客户端探测                  → embedded.MockClient ticker（interval 30s，2x2 交叉）
是否启动 mock 操作进行探测（参数）         → config.MockProbeConfig.Enabled（默认 false）
开关关闭：客户端不发请求                   → ticker.Cancel + drain in-flight（≤1 cycle）
开关关闭：供应商不显示                     → registry 注销 + router/admin 过滤 is_self_probe=true
mock 2x2 测试通道                         → 2 providers × 2 models × 2 clients = 4 paths
开关开：可见 / 关：不可见                  → /admin/self_probe/* + self_probe_* metrics + is_self_probe 标签
用 go 实现 / 统一部署                     → internal/embedded/*.go（无 python、无 sidecar、无 docker-compose）
```

---

## 3. 关键取舍记录

- **不复用既有 `USE_NEW_PROBE_MODE`**（那是切新旧 probe 栈，跟本轮正交；不要混用）
- **不污染业务路由 / 指标**：router hot path 加 `IsSelfProbe bool` 字段（不是 metadata map），自探测 metrics 走独立 `prometheus.Registry`
- **端口冲突**：MP 强制 127.0.0.1 + auto port，杜绝与既有 9001/19080 等撞号
- **API key deny**：`__self_probe__` 内部租户在业务 auth 中间件显式 deny
- **删 python mock**（PR4）：保留 `tests/local/mocks`、`tests/stress/mocks`（这两个已经是 Go 实现），只删 `scripts/mocks/llm-mock-upstream/*.py` + nginx.conf + docker-compose

---

## 4. 用户原话 vs 现有 mock 资产 交叉表

| 用户原话要点 | 既能满足的 | 不能满足的（→ 缺口） |
|-------------|-----------|---------------------|
| 2 个 mock 供应商同步启动 | `tests/local/mocks` (3个) / `tests/stress/mocks` (1个模式最多) 都是独立进程 | **同进程 Go 实现的供应商** = 缺口 PR2 |
| 定时通过 mock 客户端探测 | `cmd/routing-test-client` 是"一次性 N 轮" | **永驻 ticker 客户端** = 缺口 PR3 |
| 单一开关控制 mock 探测 | 无任何集中开关 | **config.MockProbeConfig** = 缺口 PR1 |
| 关闭时客户端静默 | 无 | **静默闸** = 缺口 PR3 |
| 关闭时供应商不显示 | 无 | **registry 注销 + 业务过滤** = 缺口 PR2 |
| mock 2×2 交叉路径 | 无 | **4 paths (A×models × B×models)** = 缺口 PR2+PR3 |
| 可见/不可见切换 | 无 | **可见性矩阵**（FR-5）= 缺口 PR2/PR3 |
| Go 实现 / 统一部署 | `tests/local/mocks`、`tests/stress/mocks` 已 Go 但独立进程；`scripts/mocks/llm-mock-upstream/*.py` Python | **同进程内嵌** = 缺口 PR1（核心） |