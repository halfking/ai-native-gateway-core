# Phase 1.5 执行检查清单

> **关联文档**: [phase1.5-revised-plan-20260625.md](./phase1.5-revised-plan-20260625.md)

## Day 1-2: 凭据管理完整迁移

### R1.1: 迁移 credentialstate/writer.go

- [ ] 复制 `credentialstate/writer.go` → `domains/credential/writer.go`
- [ ] 复制 `credentialstate/writer_test.go`
- [ ] 复制 `credentialstate/writer_regression_test.go`
- [ ] 更新 import 路径
- [ ] 修复 package 声明
- [ ] `go build ./domains/credential/` 通过
- [ ] `go test ./domains/credential/ -run TestWriter` 通过
- [ ] **目标行数: ≥ 431 行**

### R1.2: 完整迁移 circuit/breaker.go（替换 98 行简化版）

- [ ] 备份当前简化版 `domains/credential/breaker.go`
- [ ] 复制 `circuit/breaker.go` → `domains/credential/breaker.go`
- [ ] 复制 `circuit/breaker_test.go`
- [ ] 更新 import 路径
- [ ] 修复 package 声明
- [ ] 删除备份
- [ ] `go build ./domains/credential/` 通过
- [ ] `go test ./domains/credential/ -run TestBreaker` 通过
- [ ] **目标行数: ≥ 475 行**（不是 98 行）
- [ ] **必须包含: `StateQuarantined` 常量**
- [ ] **必须包含: 指数退避策略**

### R1.3: 完整迁移 limiter/（替换 63 行接口版）

- [ ] 备份当前接口版 `domains/credential/limiter.go`
- [ ] 复制 `limiter/limiter.go` → `domains/credential/limiter.go`
- [ ] 复制 `limiter/limiter_test.go`
- [ ] 复制 `limiter/limiter_concurrent_test.go`
- [ ] 复制 `limiter/redis_identity.go`
- [ ] 更新 import 路径
- [ ] 修复 package 声明
- [ ] 删除备份
- [ ] `go build ./domains/credential/` 通过
- [ ] `go test ./domains/credential/ -run TestLimiter` 通过
- [ ] **目标行数: ≥ 444 行**（不是 63 行）
- [ ] **必须包含: redis_identity.go**

### Day 1-2 完成验证

- [ ] `domains/credential/` 总行数 ≥ **2,858 行**
- [ ] `go test ./domains/credential/ -v` 全部通过
- [ ] 测试覆盖率 ≥ 85%
- [ ] 无循环依赖

---

## Day 3-4: Executor 完整迁移

### R1.4: 迁移 routing/executor_*.go

- [ ] 创建 `domains/streaming/executors/` 目录
- [ ] 复制 14 个 executor 文件
- [ ] 更新 import 路径
- [ ] 修复 package 声明为 `executors`
- [ ] `go build ./domains/streaming/executors/` 通过
- [ ] `go test ./domains/streaming/executors/ -v` 全部通过
- [ ] **目标行数: ≥ 4,361 行**
- [ ] **必须包含: executor_chat.go (973 行)**
- [ ] **必须包含: executor_anthropic.go (880 行)**

### R1.5: 迁移 relay/handler.go + stream_*.go

- [ ] 复制 `relay/handler.go` → `domains/streaming/handler.go`
- [ ] 复制 `relay/responses_stream.go`
- [ ] 复制 `relay/stream_*.go`（非测试文件）
- [ ] 更新 import 路径
- [ ] 修复 package 声明为 `streaming`
- [ ] `go build ./domains/streaming/` 通过
- [ ] `go test ./domains/streaming/ -v` 全部通过
- [ ] **目标行数: ≥ 3,116 行（handler.go）**

### Day 3-4 完成验证

- [ ] `domains/streaming/` 总行数 ≥ **7,477 行**（不是 402 行）
- [ ] 包含完整的 executor_* 实现
- [ ] 包含 relay/handler.go 核心处理器
- [ ] E2E 测试通过

---

## Day 5-6: 压缩 + 协议转换完整迁移

### R1.6: 迁移 compressor/ 核心实现（替换 604 行简化版）

- [ ] 备份当前简化版 `domains/hooks/compression/`
- [ ] 复制 `compressor/*` → `domains/hooks/compression/`
- [ ] 更新 import 路径
- [ ] 修复 package 声明为 `compression`
- [ ] `go build ./domains/hooks/compression/` 通过
- [ ] `go test ./domains/hooks/compression/ -v` 全部通过
- [ ] **目标行数: ≥ 3,000 行**（不是 604 行）
- [ ] **必须包含 6 种压缩模式**:
  - [ ] `ModeOff` (0)
  - [ ] `ModeAutoThreshold` (1)
  - [ ] `ModeOn4xx` (2)
  - [ ] `ModeDeltaOnly` (3)
  - [ ] `ModeSmart` (4)
  - [ ] `ModeAggressive` (5)
- [ ] **必须包含: Rebuilder 集成**
- [ ] **必须包含: Memora L1 集成**

### R1.7: 迁移 routing/context_summarize.go

- [ ] 复制 `routing/context_summarize.go` → `domains/hooks/compression/context_summarize.go`
- [ ] 复制 `routing/context_summarize_test.go`
- [ ] 更新 import 路径
- [ ] 修复 package 声明为 `compression`
- [ ] `go build ./domains/hooks/compression/` 通过
- [ ] `go test ./domains/hooks/compression/ -run TestContextSummarize` 通过
- [ ] **目标行数: ≥ 1,368 行**
- [ ] **必须包含: 三层压缩策略**

### R1.8: 迁移 relay/anthropic_*.go

- [ ] 创建 `domains/transformation/anthropic/` 目录
- [ ] 复制 21 个 anthropic 文件
- [ ] 更新 import 路径
- [ ] 修复 package 声明为 `anthropic`
- [ ] `go build ./domains/transformation/anthropic/` 通过
- [ ] `go test ./domains/transformation/anthropic/ -v` 全部通过
- [ ] **目标行数: ≥ 5,055 行**

### Day 5-6 完成验证

- [ ] `domains/hooks/compression/` 总行数 ≥ **4,972 行**（不是 604 行）
- [ ] `domains/transformation/anthropic/` ≥ 5,055 行
- [ ] 所有 6 种压缩模式可用
- [ ] Anthropic 协议完整支持

---

## Day 7: 剩余横切关注点 + Pipeline 集成

### R1.9: 迁移 telemetry/

- [ ] 创建 `domains/hooks/observability/telemetry/`
- [ ] 复制 `telemetry/*`
- [ ] 更新 import 路径
- [ ] `go build ./domains/hooks/observability/telemetry/` 通过
- [ ] `go test ./domains/hooks/observability/telemetry/ -v` 通过
- [ ] **目标行数: ≥ 3,270 行**

### R1.10: 补齐 audit/

- [ ] 复制 `audit/*` → `domains/hooks/audit/`
- [ ] 更新 import 路径
- [ ] `go build ./domains/hooks/audit/` 通过
- [ ] `go test ./domains/hooks/audit/ -v` 通过
- [ ] **目标行数: ≥ 2,444 行**

### R1.11: 补齐 transform/transport

- [ ] 复制 `transform/*` → `domains/transformation/`
- [ ] 复制 `transport/*` → `domains/transformation/`
- [ ] 更新 import 路径
- [ ] `go build ./domains/transformation/` 通过
- [ ] `go test ./domains/transformation/ -v` 通过
- [ ] **目标行数: ≥ 6,223 行**

### R1.12: 集成 Pipeline 到 cmd/gateway/main.go

- [ ] 创建 Pipeline 实例
- [ ] 注册 7 个 Stage:
  - [ ] Stage 1: Authentication
  - [ ] Stage 2: Pre-Routing (Identity + Session)
  - [ ] Stage 3: Routing
  - [ ] Stage 4: Credential
  - [ ] Stage 5: Transform
  - [ ] Stage 6: Upstream (Executor)
  - [ ] Stage 7: Response
- [ ] 启动成功，无编译错误
- [ ] E2E 测试通过

### R1.13: 删除旧代码（移至 _deprecated/）

- [ ] 创建 `_deprecated/` 结构
- [ ] 移动 18 个旧包
- [ ] 更新所有 import 路径
- [ ] `go build ./...` 通过
- [ ] 所有测试通过
- [ ] Git tag: `phase1.5-complete-20260625`

### Day 7 完成验证

- [ ] Pipeline 集成到 main.go
- [ ] 旧代码移至 `_deprecated/`
- [ ] `go build ./...` 成功
- [ ] 所有测试通过

---

## 整体验收标准

### 代码复用率

- [ ] 整体复用率 ≥ **70%**（当前 20.1%）
- [ ] `domains/` 总行数 ≥ **46,000**（当前 13,279）
- [ ] 核心领域完成度: **9/9**

### 功能完整性

- [ ] credential 完整迁移: ≥ 2,858 行
- [ ] streaming 完整迁移: ≥ 7,477 行
- [ ] compression 完整迁移: ≥ 4,972 行
- [ ] transformation 完整迁移: ≥ 11,278 行
- [ ] audit 完整迁移: ≥ 2,444 行
- [ ] observability 完整迁移: ≥ 3,845 行

### 测试指标

- [ ] 测试覆盖率 ≥ 85%
- [ ] 循环依赖: 0
- [ ] E2E 测试: 全部通过
- [ ] P99 延迟: ≤ 旧版 + 10ms

### 验证命令

```bash
# 1. 复用率
OLD_LINES=$(find _deprecated identity sessions auth routing credentialstate circuit limiter provider relay transform transport compressor cache observability audit telemetry security -name "*.go" 2>/dev/null | xargs wc -l 2>/dev/null | tail -1 | awk '{print $1}')
NEW_LINES=$(find domains -name "*.go" 2>/dev/null | xargs wc -l 2>/dev/null | tail -1 | awk '{print $1}')
echo "复用率: $(echo "scale=1; $NEW_LINES * 100 / $OLD_LINES" | bc)%"

# 2. 测试
go test ./... -count=1 -timeout=300s

# 3. 编译
go build ./cmd/gateway/

# 4. 启动
./gateway &
sleep 5
curl -i http://localhost:8781/healthz

# 5. E2E
./scripts/e2e-core-domains.sh
```

---

## Git Tag 计划

- [ ] `phase1.5-day2-complete-20260625` — Day 1-2 完成
- [ ] `phase1.5-day4-complete-20260625` — Day 3-4 完成
- [ ] `phase1.5-day6-complete-20260625` — Day 5-6 完成
- [ ] `phase1.5-complete-20260625` — Day 7 完成

---

## Phase 2 启动条件

⚠️ **必须达到以下标准才能开始 Phase 2**:

- [ ] 整体复用率 ≥ 70%
- [ ] `domains/` 总行数 ≥ 46,000
- [ ] 所有核心功能有完整实现
- [ ] E2E 测试通过
- [ ] Pipeline 已上线

---

**检查清单完成**