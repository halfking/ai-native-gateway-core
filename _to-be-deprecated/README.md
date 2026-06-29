# _to-be-deprecated/ — R1.13 切流量前的待删除代码

> **创建日期**: 2026-06-26
> **创建原因**: Phase 1.5 R1.1-R1.11 完成成熟代码迁移到 `domains/*`, 老包暂留以维持
> cmd/gateway/main.go 生产可用。本目录标记最终将被删除的代码, 等待 R1.13 切流量。
>
> **删除时间表**: 待用户授权 R1.13 后执行 (预计 2026-07 后)

---

## 1. 当前状态

> **2026-06-26 replacement audit update**: `_to-be-deprecated/` 本身就是待删除目录。
> 当前不再把文件二次移动到新目录。删除候选按
> `docs/architecture/legacy-replacement-audit-20260626.md` 维护。

### 1.1 已迁移 (本目录内)
| 路径 | 原位置 | 性质 | 迁移日期 |
|------|--------|------|----------|
| `observability/siem/` | `observability/siem/` | NEW (2026-06-24) 但 0 外部 import, 未集成 | 2026-06-26 |
| `orphan-tests/model-routing-test.go` | 顶层 `model-routing-test.go` | 顶层 package main 孤立测试运行器, 非测试文件但按测试用 | 2026-06-26 |

### 1.2 待删除包状态 (R1.13 触发, 当前阻塞)
| 老包 | 新位置 | 状态 | 外部 import 数 | 阻塞原因 |
|------|--------|------|---------------|----------|
| `audit/` | `domains/hooks/audit/` | ✅ 新包已上线 | **0** | 旧 `relay/routing/transport` 内部仍引用 |
| `auth/` | `domains/authentication/` | ✅ 新包已上线 | **0** | 旧 `relay/sessions` 内部仍引用 |
| `circuit/` | `domains/credential/breaker.go` | ✅ 重构完成 | **0** | 旧 `relay/routing` 内部仍引用 |
| `compressor/` | `domains/hooks/compression/` | ✅ 新包已上线 | **0** | 旧 `relay/routing` 内部仍引用 |
| `credentialstate/` | `domains/credential/writer.go` | ✅ **DELETED (R1.13 2026-06-29)** | **0** | — |
| `identity/` | `domains/identity/` | ✅ 新包已上线 | **0** | 旧 `relay/routing` 内部仍引用 |
| `limiter/` | `domains/credential/limiter.go` | ✅ 重构完成 | **0** | 旧 `relay/routing` 内部仍引用 |
| `memora/` | `domains/memory/client/` | ✅ **DELETED (R1.13 2026-06-29)** | **0** | — |
| `relay/` | `domains/streaming/` + `domains/transformation/anthropic/` | ✅ 新包已上线 | **0** | 旧 `transport` 内部仍引用 |
| `routing/` | `domains/streaming/executors/` | ✅ **DELETED (R1.13 2026-06-29)** | **0** | — |
| `sessions/` | `domains/session/` | ✅ 新包已上线 | **0** | 旧 `relay/routing` 内部仍引用 |
| `telemetry/` | `domains/hooks/observability/telemetry/` | ✅ **DELETED (R1.13 2026-06-29)** | **0** | — |
| `transform/` | `domains/transformation/transform*.go` | ✅ 重构完成 | **0** | 旧 `compressor/relay/routing` 内部仍引用 |
| `transport/` | `domains/transformation/transport*.go` | ✅ **DELETED (R1.13 2026-06-29)** | **0** | — |

**R1.13 进度 (2026-06-29)**: 5 个老包已删除（credentialstate, routing, memora, telemetry, transport），删除约 22,822 LOC。
剩余 9 个老包（audit, auth, circuit, compressor, identity, identitypool, limiter, observability/siem, relay, sessions, transform）共 43,926 LOC。

**汇总**: 14 个老包中已有 5 个完成 R1.13 清理。剩余老包仍被旧包内部依赖链引用。

---

## 2. R1.13 切流量前置条件

### 2.1 必须达成
- [ ] cmd/gateway/main.go 改用 `domain.PipelineRequest` + `pipeline.RequestPipeline` (R1.12)
  - 当前: R1.12 仅提供旁路 feature flag (`LLM_GATEWAY_V2_ENABLED=true`)
  - 需: 把 v1 main.go 的 1663 行整体重写为 Pipeline 入口
- [ ] 全量 import 重写 (老包 → domains/*)
  - 涉及 14 个老包 × 平均 15-30 处 import
  - 自动化: `sed -i '' 's|kaixuan/llm-gateway-go/routing|kaixuan/llm-gateway-go/domains/streaming|g' ...`
  - 手动调整: 方法调用差异 (如 Executor.Method → free function)
- [ ] E2E 验证 `./scripts/e2e-llm-gateway-go-multitenant-isolation.sh` PASS
- [ ] 11 项 DB-backed verify_chain PASS (见 `scripts/_lib/llmgw-deploy-lib.sh`)
- [ ] 灰度切流: 184 staging 1% 流量跑 24h → 100%

### 2.2 风险评估
| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| import 循环依赖 | 中 | 高 | 保留老包作为依赖, 逐步解耦 |
| 公共符号签名不兼容 | 高 | 中 | 在切换前用 go vet -composites 全面检查 |
| 测试覆盖率骤降 | 中 | 中 | 双轨期跑新旧两套测试 |
| 性能退化 | 低 | 高 | P99 latency 基线对比 |
| E2E 失败 | 中 | 高 | 先 staging 验证再生产 |

---

## 3. 目录结构 (按原结构 mirror)

```
_to-be-deprecated/
├── README.md                    ← 本文件
├── MIGRATION-MANIFEST.md        ← 详细迁移清单
├── audit/                       ← 14 个老包, R1.13 后填充
├── auth/
├── circuit/
├── compressor/
├── credentialstate/
├── identity/
├── limiter/
├── memora/
├── observability/
│   └── siem/                    ← ✅ 已迁 (5 文件)
├── relay/
├── routing/
├── sessions/
├── telemetry/
├── transform/
├── transport/
└── orphan-tests/
    └── model-routing-test.go    ← ✅ 已迁 (1 文件)
```

---

## 4. 已迁移文件详情

### 4.1 observability/siem/ (5 文件, 0 外部 import)
- `siem.go` — SIEM 集成核心 (CEF/LEEF formatter)
- `siem_test.go`
- `config.go` — SIEM 配置 schema
- `config_test.go`
- `design_intent_test.go`

**迁移理由**: 包级 0 import, 不在 cmd/gateway/main.go 依赖图内。但作为新功能,
不应长期待在 _to-be-deprecated/。后续集成时 (合规/等保 2.0 阶段) 应迁移到 `domains/hooks/observability/siem/`。

### 4.2 orphan-tests/model-routing-test.go (1 文件)
- 原位置: 顶层 `model-routing-test.go` (`package main`)
- 用途: 多轮 routing 测试运行器 (`go run model-routing-test.go`)
- 迁移理由: 顶层孤立文件, 不属于任何构建目标, 不会影响任何 build
- 后续: 若需保留, 应迁到 `tests/integration/` 或 `scripts/tests/`

---

## 5. 自动化迁移 (R1.13 时执行)

```bash
# 1. 创建目录 (R1.13 时已存在)
mkdir -p _to-be-deprecated/{audit,auth,circuit,compressor,credentialstate,
  identity,limiter,memora,relay,routing,sessions,telemetry,transform,transport}

# 2. 迁移文件 (保留 .git 历史: git mv)
git mv audit _to-be-deprecated/audit
git mv auth _to-be-deprecated/auth
git mv circuit _to-be-deprecated/circuit
# ... 14 个老包

# 3. 更新所有 import (main.go + 其他 11 个外部 import 文件)
# 自动化脚本参考 phase1.5-revised-plan-20260625.md §5.5

# 4. 验证
go build ./...
go test ./...
make -C scripts lint-tenant-scope-llmgw  # L1=0
make -C scripts lint-llmgw-deploy        # PASS
```

---

## 6. 相关文档

- Phase 1.5 完整计划: `docs/architecture/phase1.5-revised-plan-20260625.md`
- Phase 1.5 执行审计: `docs/architecture/phase1-execution-audit-v2-revised-20260625.md`
- Phase 1.5 checklist: `docs/architecture/phase1.5-checklist-20260625.md`
- R1.12 本地测试方案: `docs/architecture/phase2-r112-local-test-plan.md`
- R1.12 部署红线: `docs/architecture/2026-06-22-r112-deploy-red-lines.md`
- 5 agent reports (2026-06-25)

---

**最后更新**: 2026-06-26 (Round 49 R1.13 准备阶段)
**下次更新**: R1.13 切流量完成后
