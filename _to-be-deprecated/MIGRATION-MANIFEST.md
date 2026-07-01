# _to-be-deprecated MIGRATION MANIFEST

> **日期**: 2026-06-26
> **关联**: Phase 1.5 R1.13 切流量 + AGENTS.md §"分支冻结期"

## 1. 总览

| 指标 | 数值 |
|------|------|
| 老包总数 | 14 |
| 需迁移 .go 文件 | ~220 (含 ~150 测试文件) |
| 外部 import 数 (cmd/gateway/main.go) | 40+ |
| 涉及外部文件 (除 main.go) | 11+ |
| 估计工作量 | 4-6h (自动化 sed + 手工调整 + 测试) |

> **2026-06-26 audit correction**: 14 个老包当前已经位于 `_to-be-deprecated/`
> 待删除目录。`go test ./...` 不会遍历下划线目录，必须显式测试
> `./_to-be-deprecated/<pkg>`。最新替代性审计见
> `docs/architecture/legacy-replacement-audit-20260626.md`。

## 2. 详细映射

### 2.1 audit/ → domains/hooks/audit/

**老包**: `audit/`
**新包**: `domains/hooks/audit/`
**外部 import**: 46 处 (cmd/gateway/main.go 23 + admin/* 8 + 其他 15)
**等价 API**: Event, Sink, BatchWriter, JSONSink, MultiSink, LogSink
**状态**: 新包 9 个文件, 5 子目录, 完整等价

**迁移动作**:
```bash
git mv audit _to-be-deprecated/audit
# import 重写: 46 处 'kaixuan/llm-gateway-go/audit' → 'kaixuan/llm-gateway-go/domains/hooks/audit'
```

### 2.2 auth/ → domains/authentication/

**老包**: `auth/` (1 个 .go 文件)
**新包**: `domains/authentication/`
**外部 import**: 19 处
**等价 API**: Verifier
**状态**: 新包上线

### 2.3 circuit/ → domains/credential/breaker.go

**老包**: `circuit/breaker.go` (475 行, 完整版)
**新位置**: `domains/credential/breaker.go` (475 行, 同步)
**外部 import**: 19 处
**等价 API**: Breaker, StateClosed/Open/HalfOpen/Quarantined
**状态**: R1.2 已重构, 已通过验证

### 2.4 compressor/ → domains/hooks/compression/

**老包**: `compressor/` (19 + 15 _test = 34 文件)
**新包**: `domains/hooks/compression/` (135+ 文件)
**外部 import**: 6 处
**等价 API**: Compressor, Compactor, LCS, RecoveryCoordinator
**状态**: R1.6 已重构, 226 tests PASS

**特殊注意**: 
- `compressor/metrics.go` 用 `MustRegister` 与新包 `domains/hooks/compression/metrics.go` 冲突
- 已通过 `AlreadyRegisteredError` 兜底解决 (Round 49 commit 4)
- R1.13 删旧包后可移除兜底

### 2.5 credentialstate/ → domains/credential/writer.go

**老包**: `credentialstate/writer.go` (431 行)
**新位置**: `domains/credential/writer.go`
**外部 import**: 6 处

### 2.6 identity/ → domains/identity/

**老包**: `identity/` (2 文件)
**新包**: `domains/identity/`
**外部 import**: 18 处

### 2.7 limiter/ → domains/credential/limiter.go

**老包**: `limiter/` (4 文件)
**新位置**: `domains/credential/{limiter,redis_identity}.go`
**外部 import**: 22 处

### 2.8 memora/ → domains/integration/

**老包**: `memora/` (5 + 4 _test = 9 文件)
**新包**: 部分在 `domains/integration/`
**外部 import**: 18 处
**状态**: 部分迁移, 需手动拆分剩余功能

### 2.9 relay/ → domains/streaming/ + domains/transformation/anthropic/

**老包**: `relay/` (40 + 56 _test = 96 文件)
**新包**: 
- `domains/streaming/` (handler.go 3116 行 + stream 相关)
- `domains/streaming/executors/` (14 个 executor_*.go)
- `domains/transformation/anthropic/` (21 个 anthropic_*.go)
**外部 import**: 4 处
**状态**: R1.4 + R1.5 + R1.8 已重构, 复制未替换 (30 个依赖文件)

**特殊注意**:
- 原 R1.4 复制未替换, 30 个文件在 streaming + streaming/executors
- 需删除原 routing/ relay/ 后才能完整迁移

### 2.10 routing/ → domains/streaming/ + domains/routing/

**老包**: `routing/` (14 + 25 _test = 39 文件)
**新包**: 
- `domains/streaming/executors/` (部分)
- `domains/routing/` (新)
**外部 import**: 13 处

**特殊注意**:
- routing/context_summarize.go (1368 行) 与 domains/hooks/compression/compaction.go (691 行) 等价 (5 文件 1769 行)
- 接受等价替代 (Round 49 Step B 决策)

### 2.11 sessions/ → domains/session/

**老包**: `sessions/` (7 文件)
**新包**: `domains/session/`
**外部 import**: 18 处

### 2.12 telemetry/ → domains/hooks/observability/telemetry/

**老包**: `telemetry/` (4 + 5 _test = 9 文件)
**新包**: `domains/hooks/observability/telemetry/`
**外部 import**: 18 处

### 2.13 transform/ → domains/transformation/

**老包**: `transform/` (4 + 4 _test = 8 文件)
**新包**: `domains/transformation/transform*.go` (8 文件)
**外部 import**: 24 处

### 2.14 transport/ → domains/transformation/

**老包**: `transport/` (10 + 9 _test = 19 文件)
**新包**: `domains/transformation/transport*.go` (19 文件)
**外部 import**: 1 处

## 3. 实施步骤 (R1.13 触发时)

### Step 1: 准备
```bash
# 备份当前 main
git tag phase1.5-pre-r113-$(date +%Y%m%d)

# 验证 R1.12 已完成 (v2 feature flag 可用)
git log --grep="R1.12 v2 Pipeline feature flag" --oneline -1
```

### Step 2: 自动化迁移
```bash
# 创建 _to-be-deprecated/ 子目录 (已存在, 验证)
ls _to-be-deprecated/

# 移动老包 (保留 git 历史)
for pkg in audit auth circuit compressor credentialstate identity limiter memora relay routing sessions telemetry transform transport; do
  if [ -d "$pkg" ] && [ ! -d "_to-be-deprecated/$pkg" ]; then
    git mv "$pkg" "_to-be-deprecated/$pkg"
  fi
done
```

### Step 3: import 重写
```bash
# 自动化 sed (40+ 处)
find . -name '*.go' -not -path './_to-be-deprecated/*' -exec sed -i '' \
  -e 's|"github.com/kaixuan/llm-gateway-go/audit|"github.com/kaixuan/llm-gateway-go/domains/hooks/audit|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/auth|"github.com/kaixuan/llm-gateway-go/domains/authentication|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/circuit|"github.com/kaixuan/llm-gateway-go/domains/credential|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/compressor|"github.com/kaixuan/llm-gateway-go/domains/hooks/compression|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/credentialstate|"github.com/kaixuan/llm-gateway-go/domains/credential|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/identity|"github.com/kaixuan/llm-gateway-go/domains/identity|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/limiter|"github.com/kaixuan/llm-gateway-go/domains/credential|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/memora|"github.com/kaixuan/llm-gateway-go/domains/integration|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/relay|"github.com/kaixuan/llm-gateway-go/domains/streaming|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/routing|"github.com/kaixuan/llm-gateway-go/domains/streaming|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/sessions|"github.com/kaixuan/llm-gateway-go/domains/session|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/telemetry|"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/transform|"github.com/kaixuan/llm-gateway-go/domains/transformation|g' \
  -e 's|"github.com/kaixuan/llm-gateway-go/transport|"github.com/kaixuan/llm-gateway-go/domains/transformation|g' \
  {} \;
```

### Step 4: 手工调整
- 修复 Executor.Method → free function 调用差异
- 修复 receiver 类型变化 (e.g., `*Executor` → `*Dependencies`)
- 修复 hook 签名变化 (e.g., 新增 r.Context 参数)

### Step 5: 验证
```bash
# 编译
go build ./...

# 单元测试
go test ./...

# 9 linter
make -C scripts lint-{crm-go,tenant-scope-brandmind,tenant-scope-llmgw,
  pg-rls,otel-tenant,k8s-yaml,deploy,llmgw-deploy,deploy-ssot}

# 11 项 verify_chain (llm-gateway-go 部署前)
llmgw::verify_chain
```

### Step 6: 灰度切流 (需用户授权)
1. 184 k3s 部署新 binary, traffic 1% 路由 /v2/*
2. 监控 24h, P99 延迟, error rate
3. 通过 → 100% 切流
4. 删 v1 main.go + 旧 packages

## 4. 阻塞因素 (必须先解决)

1. **R1.12 完整集成** (cmd/gateway/main.go 整体重写)
   - 当前: 旁路 feature flag (`LLM_GATEWAY_V2_ENABLED=true`)
   - 需: 整体切换到 v2 Pipeline

2. **transformation metrics 冲突** (Round 49 发现的预存 bug)
   - `domains/transformation/metrics.go` 用 `transport_*` 前缀与 `transport/metrics.go` 冲突
   - 修复: 改用 private registry

3. **完整 E2E 测试** (生产等价性)
   - 多租户隔离
   - 流式响应
   - 工具调用
   - 错误恢复
   - 审计/可观测性

## 5. 当前可删除候选与阻塞

### 5.1 可删除候选 (需 owner 最终授权)

| 路径 | 原因 |
|------|------|
| `_to-be-deprecated/telemetry/` | 新 `domains/hooks/observability/telemetry/` 已存在；无外部 import；无旧包内部反向引用 |
| `_to-be-deprecated/transport/` | 新 `domains/transformation/` 已存在；无外部 import；无旧包内部反向引用 |

### 5.2 暂不可删除

其余 `_to-be-deprecated/*` 老包仍被旧包内部依赖链引用，尤其是
`relay/`、`routing/`、`compressor/` 之间的依赖链。删除这些包前必须先完成
旧链路整体切断或继续重写内部 imports。

### 5.3 2026-07-01 Phase 0 收尾新增候选废弃

| 路径 | 状态 | 决策文档 |
|------|------|---------|
| `_to-be-deprecated/flowcontrol-候选废弃-20260701/` | 孤立包 (无外部 import) | commit 7a9e1941 |
| `_to-be-deprecated/taskmanagement-候选废弃-20260701/` | 孤立包 (无外部 import) | commit 7a9e1941 |
| `_to-be-deprecated/compliance-候选废弃-20260701/` | 孤立包 (无外部 import) | opencode-agent 提交 |
| `_to-be-deprecated/llmclient-候选废弃-20260701/` | 孤立包 (无外部 import) | opencode-agent 提交 |
| `_to-be-deprecated/notification-候选废弃-20260701/` | 孤立包 (1624 行 LarkBotChannel 完整实现) | `docs/产品方案/2026-07-01-notification-decision.md` |

所有上述包均符合"完整实现 + 0 外部 import"模式，统一加日期后缀
`*-候选废弃-20260701/`。待 owner 复核后可安全删除。

### 5.4 仍在顶层但不是删除候选

| 路径 | 状态 |
|------|------|
| `cache/` | 0 外部 import，但 `cache/prefix` 与 `cache/semantic` 具体逻辑尚未等价迁入 `domains/hooks/cache` |
| `security/armor` | 仍被 `cmd/gateway/main.go` 与 `domains/streaming/handler.go` 引用 |

## 6. 当前已迁移文件 (5 个 + 1 个)

详见 `_to-be-deprecated/README.md` §4。

---

**最后更新**: 2026-07-01
**下次更新**: Phase 2 Go module 拆分时 review 所有 `*-候选废弃-20260701/` 做最终去留
