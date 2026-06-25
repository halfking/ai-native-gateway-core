# LLM Gateway Go 领域驱动架构重构

> **状态**: ✅ Phase 1.5 R1.1-R1.11 完成 (2026-06-25) + R1.12 旁路完成 (2026-06-26)
> **日期**: 2026-06-26 (Round 49)
> **团队**: AI Architecture Team
> **下一步**: R1.13 切流量 (用户授权后)

## 📋 文档导航

| 文档 | 描述 | 状态 |
|------|------|------|
| [domain-refactoring-plan.md](./domain-refactoring-plan.md) | 完整架构设计方案 | ✅ 已完成 |
| [implementation-plan.md](./implementation-plan.md) | 详细实施计划 | ✅ 已完成 |
| [phase1.5-revised-plan-20260625.md](./phase1.5-revised-plan-20260625.md) | Phase 1.5 修订计划 R1.1-R1.13 | ✅ 已完成 |
| [phase1-execution-audit-v2-revised-20260625.md](./phase1-execution-audit-v2-revised-20260625.md) | 执行审计 v2 | ✅ 已完成 |
| [phase2-r112-local-test-plan.md](./phase2-r112-local-test-plan.md) | R1.12 本地测试方案 | ✅ 已完成 |
| [_to-be-deprecated/README.md](../../_to-be-deprecated/README.md) | R1.13 待删除代码迁移说明 | ✅ 已完成 |
| [_to-be-deprecated/MIGRATION-MANIFEST.md](../../_to-be-deprecated/MIGRATION-MANIFEST.md) | 14 个老包详细迁移清单 | ✅ 已完成 |

---

## 🎯 重构目标

### Round 49 进度 (2026-06-26)

| 任务 | 状态 | 详情 |
|------|------|------|
| Phase 1.5 R1.1-R1.11 迁移 | ✅ | 23 个领域包, 46k 行, 651 tests PASS |
| R1.12 旁路 feature flag | ✅ | cmd/gateway/main_v2_pipeline.go (16 stages) |
| R1.12 本地测试环境 | ✅ | docker-compose + 4 scripts |
| Admin tenant scope 修复 | ✅ | 14 个 handler, L1 14→0 |
| RLS migration 052 | ✅ | candidate_failure_logs + request_wal, L1 2→0 |
| deploy-mobile-h5 SSOT | ✅ | lint-deploy-ssot L1 1→0 |
| 9 blocking linter | ✅ | 全部 L1=0 |
| _to-be-deprecated/ 目录 | ✅ | 6 文件已迁, 14 老包待 R1.13 |
| R1.13 切流量 | ⏸️ | 待用户授权 |
| R1.12 完整集成 (v1 main.go) | ⏸️ | 待用户授权 |

### 核心目标（优先级 ABC）

**A: 测试覆盖率 90%+**
- 当前: domain 包 91.4%
- 目标: 所有领域包 ≥ 90%
- 验证: `go test ./domains/... -cover`

**B: 循环依赖为 0**
- 当前: 未知（需审计）
- 目标: 包依赖图是 DAG
- 验证: `./scripts/check-cycles.sh`

**C: 新增供应商 < 0.5 天**
- 当前: ~3 天（需改 10+ 文件）
- 目标: 0.5 天（只需修改 domains/provider/）
- 验证: 实际操作计时

---

## 🏗️ 架构概览

### 16 个领域清单

#### 核心领域（9 个）

1. **用户认证** - API Key/JWT 验证、RBAC
2. **租户管理** - 配额、策略、RLS
3. **客户识别** - 虚拟身份隧道
4. **会话管理** - gw_session_id、粘性
5. **模型路由** - 候选规划、策略
6. **凭据管理** - 熔断、并发、解密
7. **供应商管理** - 健康探测、配置
8. **协议转换** - IR 中间表示、4 象限
9. **流式转发** - SSE 中继、背压

#### 横切领域（7 个）

10. **会话缓存** - L1/L2/L3 缓存
11. **会话压缩** - LCS + LLM 总结
12. **安全检查** - 意图识别、威胁检测
13. **审计日志** - 批量写入、DLQ
14. **可观测性** - Prometheus、Trace
15. **工具拦截** - Meta-tool 扩展
16. **智能体生态** - 发现、行为分析、能力集成

---

## 📅 时间线（3 周 / 4 个 Phase）

### Phase 0: 准备阶段（2 天）
- 创建新目录结构
- 实现事件总线和 Hook Pipeline
- 设置 CI 验证

### Phase 1: 核心领域迁移（1 周）
- 9 个核心领域迁移
- 3 个 AI Agent 并行工作
- 复用现有代码 70%+

### Phase 2: 横切领域迁移（1 周）
- 7 个横切领域迁移
- 接入 Hook Pipeline
- 保持性能不退化

### Phase 3: 智能体生态与会话检查器（4 天）
- 智能体注册表和行为分析
- 可插拔会话检查器框架
- MCP/技能集成中心

### Phase 4: 清理与优化（2 天）
- 删除旧代码
- 性能优化
- 文档完善

---

## 🔑 关键设计决策

### 1. 事件驱动架构

**选择**: 内存事件总线（快速通道）+ Redis（持久化）

**理由**:
- 领域间松耦合
- 支持异步处理
- 便于扩展新领域

### 2. Hook Pipeline 模式

**选择**: 14 个阶段的 Hook Pipeline

**理由**:
- 横切关注点可插拔
- 同优先级并行执行
- 易于测试和调试

### 3. IR 中间表示

**选择**: 保留现有 transport/ 的 IR 层

**理由**:
- 已有 91.4% 测试覆盖
- 4 象限转换逻辑完善
- 支持灰度切换

### 4. 现有代码复用

**策略**: 70% 直接移动 + 20% 重组 + 10% 新增

**理由**:
- 降低风险
- 保留已验证逻辑
- 快速交付

---

## 📊 代码复用矩阵

| 现有包 | 操作 | 目标位置 | 复用率 |
|--------|------|----------|--------|
| identity/ | 移动 | domains/identity/ | 95% |
| auth/ | 移动 | domains/authentication/ | 100% |
| provider/ | 移动 | domains/provider/ | 100% |
| compressor/ | 移动 | domains/hooks/compression/ | 100% |
| sessions/ | 拆分 | domains/session/ + hooks/cache/ | 80% |
| routing/ | 拆分 | domains/routing/ + streaming/ | 60% |
| credentialstate/ + circuit/ + limiter/ | 合并 | domains/credential/ | 70% |
| transform/ + transport/ | 合并 | domains/transformation/ | 90% |
| relay/ | 拆分 | domains/streaming/ + hooks/tools/ | 70% |
| 🆕 智能体生态 | 新增 | domains/agent-ecosystem/ | 0% |
| 🆕 会话检查器 | 新增 | domains/hooks/session-inspector/ | 0% |
| 🆕 Pipeline | 新增 | domains/pipeline/ | 0% |
| 🆕 事件总线 | 新增 | eventbus/ | 0% |

**总体复用率**: 约 70%

## 📂 当前目录结构 (Round 49)

```
llm-gateway-go/
├── cmd/
│   ├── gateway/               # v1 生产入口 (1663 行, 待 R1.13 重写)
│   ├── gateway-v2/           # v2 Pipeline 入口 (393 行, R1.12 验证用)
│   └── ...                   # 其他 cmd (probe-cred, traffic-replay 等)
├── domains/                  # ⭐ 新分层架构 (R1.1-R1.11 完成)
│   ├── authentication/        # 认证
│   ├── agent-ecosystem/       # 智能体生态
│   ├── credential/            # 凭据 (breaker + limiter + writer)
│   ├── hooks/                 # 横切关注点
│   │   ├── audit/             # 审计日志
│   │   ├── cache/             # 会话缓存
│   │   ├── compression/       # 压缩 (R1.6 + R1.7 + compaction)
│   │   ├── observability/     # 可观测性 + telemetry
│   │   ├── security/          # 安全
│   │   ├── session-inspector/ # 会话检查器
│   │   └── tools/             # 工具拦截
│   ├── identity/             # 身份
│   ├── integration/          # 集成 (含 memora 部分)
│   ├── pipeline/             # Pipeline 编排
│   ├── provider/             # 供应商
│   ├── routing/              # 路由 (新)
│   ├── session/              # 会话
│   ├── streaming/            # 流式 + executors
│   └── transformation/       # 转换 + anthropic
├── admin/                    # Admin API handlers
├── _to-be-deprecated/        # ⭐ R1.13 待删除 (Round 49 新增)
│   ├── README.md
│   ├── MIGRATION-MANIFEST.md
│   ├── observability/siem/    # ✅ 已迁 (5 文件)
│   ├── orphan-tests/          # ✅ 已迁 (model-routing-test.go)
│   └── audit/ auth/ ...      # ⏳ 14 老包待 R1.13
├── audit/  auth/  circuit/  compressor/  credentialstate/  identity/
├── limiter/  memora/  observability/  relay/  routing/  sessions/
├── telemetry/  transform/  transport/    # 14 老包 (生产 main.go 仍用)
└── ...
```

**目录组织原则**:
1. `domains/` — 新分层架构 (Domain-Driven Design)
2. `cmd/` — 所有可执行入口
3. `admin/` — Admin HTTP handlers (新)
4. `_to-be-deprecated/` — R1.13 切流量前的过渡区
5. 顶层老包 — R1.13 时整体迁入 `_to-be-deprecated/`

---

## ✅ 验收标准

### 技术指标

```bash
# 1. 测试覆盖率
go test ./domains/... -cover | grep "coverage:"
# 期望: 所有包 ≥ 90%

# 2. 循环依赖
./scripts/check-cycles.sh
# 期望: 0 cycles

# 3. 性能基准
./scripts/load-test.sh --rps 1000 --duration 5m
# 期望: P99 延迟 < 500ms, 错误率 < 0.1%

# 4. 静态检查
golangci-lint run ./domains/...
# 期望: 0 issues
```

### 业务指标

- [ ] 所有现有 API 响应格式一致
- [ ] 错误码不变
- [ ] 延迟增加 < 10ms
- [ ] 智能体注册成功率 > 99%
- [ ] 行为分析覆盖 100% 请求

---

## 🚀 快速开始

### 1. 阅读架构设计
```bash
cat docs/architecture/domain-refactoring-plan.md
```

### 2. 查看实施计划
```bash
cat docs/architecture/implementation-plan.md
```

### 3. 开始 Phase 0
```bash
# 创建目录结构
mkdir -p domains/{authentication,tenant,identity,...}

# 实现事件总线
code eventbus/memory_bus.go

# 实现 Pipeline
code domains/pipeline/pipeline.go
```

### 4. 运行验证
```bash
# 编译
go build ./domains/...

# 测试
go test ./domains/... -v -cover

# 检查循环依赖
./scripts/check-cycles.sh
```

---

## 📞 联系方式

**问题反馈**: 在相应文档中提 issue  
**架构讨论**: 团队周会  
**代码审查**: PR 模板中有详细检查清单  

---

## 📚 相关资源

- [原始架构文档](../../ARCHITECTURE.md)
- [domain/ 包 README](../../domain/README.md)
- [transport/ 包 README](../../transport/README.md)
- [_to-be-deprecated/ README](../../_to-be-deprecated/README.md) ⭐
- [_to-be-deprecated/ 迁移清单](../../_to-be-deprecated/MIGRATION-MANIFEST.md) ⭐
- [Phase 1.5 R1.13 待删除目录](https://github.com/anomalyco/opencode/wiki)

---

**最后更新**: 2026-06-26 (Round 49: Phase 1.5 完成 + R1.12 旁路 + _to-be-deprecated/ 创建)
**维护者**: AI Architecture Team
**版本**: v2.1
