# LLM Gateway 全方面测试体系 — 总览

> 完整的测试体系，包含场景测试 + 路由系统测试 + 自动化框架

---

## 📋 测试体系架构

```
docs/全方面测试/
├── README.md                           ← 本文件（入口索引）
│
├── 【原有场景测试】(16 个场景)
│   ├── 00-总览.md                       ← 场景测试执行入口
│   ├── 01-测试架构设计.md
│   ├── 02-测试环境部署.md
│   ├── 03-测试场景定义.md              ← S1-S16 详细定义
│   ├── 04-数据准备方案.md
│   ├── 05-执行流程.md
│   ├── 06-验收标准.md
│   ├── 07-故障类型矩阵.md
│   ├── 08-kill-switch.md
│   ├── 09-多模态与模型契约场景.md
│   ├── 10-多模态客户端与画像.md
│   ├── 执行提示词.md
│   │
│   ├── tools/                           ← 场景测试工具
│   │   ├── mock_supplier.py            ← 60 个 mock LLM
│   │   ├── mock_orchestrator.py        ← 状态编排
│   │   ├── start_suppliers.sh          ← 启/停 suppliers
│   │   ├── loadtest.py                 ← 负载测试
│   │   └── validation_report.py        ← 结果报告
│   │
│   ├── data/
│   │   └── seed.sql                    ← 测试数据
│   │
│   └── scenarios/                       ← 16 个可执行场景
│       ├── run_all.sh                  ← 一键运行所有场景
│       ├── S01_baseline.sh
│       ├── S02_cost_route.sh
│       └── ... (S03-S16)
│
├── 【新增：路由系统测试】(52 个用例)
│   ├── routing-test-overview.md        ← 路由测试总览（本文件后续部分）
│   ├── routing-test-guide.md           ← 详细使用指南
│   ├── routing-architecture.md         ← 架构与流程图
│   ├── routing-diagnosis.md            ← 7 个根因诊断
│   ├── routing-test-cases.md           ← 52 个用例清单
│   └── routing-ci-cd.md                ← CI/CD 集成指南
│
└── 【测试脚本位置】
    ../../tests/routing/                 ← 实际测试脚本
    ├── run_all_tests.sh                ← 路由测试运行器
    ├── lib/assert.sh                   ← 断言库
    ├── layer1_direct/                  ← 18 个直连测试
    ├── layer2_components/              ← 19 个组件测试
    └── layer3_integration/             ← 15 个集成测试
```

---

## 🎯 两套测试体系对比

### 场景测试 vs 路由测试

| 维度 | 场景测试 (S1-S16) | 路由测试 (Layer 1-3) |
|------|------------------|---------------------|
| **目标** | 端到端业务场景验证 | 路由系统深度测试 |
| **测试数量** | 16 个场景 | 52 个用例 |
| **运行时间** | 90-120 分钟 | 10-15 分钟 |
| **环境需求** | 60 个 mock suppliers | Gateway + 少数凭据 |
| **测试粒度** | 业务场景级 | 组件/功能级 |
| **适用阶段** | 发布前验证 | 开发/回归测试 |
| **自动化程度** | 全自动 | 全自动 |

### 互补关系

```
┌─────────────────────────────────────────────┐
│           完整测试金字塔                     │
├─────────────────────────────────────────────┤
│                                             │
│         /\                                  │
│        /  \                                 │
│       / 场景\      ← S1-S16 (16 个场景)     │
│      /------\                               │
│     /        \                              │
│    / Layer 3  \   ← 集成测试 (15 个)        │
│   /------------\                            │
│  /              \                           │
│ /   Layer 2      \ ← 组件测试 (19 个)       │
│/------------------\                         │
│      Layer 1       ← 直连测试 (18 个)       │
└─────────────────────────────────────────────┘
```

---

## 🚀 快速开始

### 方式 1: 运行场景测试（业务验证）

适合：发布前完整验证

```bash
# 1. 准备环境（一次性）
env-injector inject aliyun-edge-252
PGPASSWORD=<pw> psql -h localhost -p 5432 -U xutaohuang -d llm_gateway \
    -f docs/全方面测试/data/seed.sql

# 2. 启动 mock cluster（60 个进程）
docs/全方面测试/tools/start_suppliers.sh

# 3. 运行所有场景（90-120 分钟）
docs/全方面测试/scenarios/run_all.sh --gateway http://localhost:8781

# 快速模式（45-60 分钟）
docs/全方面测试/scenarios/run_all.sh --gateway http://localhost:8781 --fast
```

### 方式 2: 运行路由测试（快速验证）

适合：开发阶段回归测试

```bash
# 1. 进入测试目录
cd tests/routing

# 2. 运行所有路由测试（10-15 分钟）
./run_all_tests.sh all

# 3. 或分层运行
./run_all_tests.sh layer1  # 直连测试 (2-3 分钟)
./run_all_tests.sh layer2  # 组件测试 (3-4 分钟)
./run_all_tests.sh layer3  # 集成测试 (2-3 分钟)

# 快速模式（跳过性能测试）
export SKIP_LONG_TESTS=true
./run_all_tests.sh all
```

### 方式 3: 组合运行（完整验证）

```bash
# 1. 先运行快速路由测试（10 分钟）
cd tests/routing
export SKIP_LONG_TESTS=true
./run_all_tests.sh all

# 2. 再运行关键场景（选择性）
cd ../../docs/全方面测试/scenarios
./S01_baseline.sh
./S06_mixed_fault.sh
./S12_comprehensive.sh
```

---

## 📊 测试覆盖总览

### 完整测试矩阵

| 测试类型 | 用例数 | 运行时间 | 覆盖范围 | 状态 |
|---------|-------|---------|---------|------|
| **场景测试** | 19 | 100-130 min | 业务场景 | ✅ |
| **路由测试** | 52 | 10-15 min | 路由系统 | ✅ |
| **总计** | **71** | **110-145 min** | **全面** | ✅ |

### 场景测试覆盖（19 个）

| 场景 | 描述 | 预期结果 |
|------|------|---------|
| S01 | 基准性能 | 成功率 ≥ 99% |
| S02 | 成本优化路由 | 优先低成本 |
| S03 | 并发差异化 | 并发限流生效 |
| S04 | 配额耗尽故障转移 | 自动切换 |
| S05 | 质量惩罚 | 降权慢/不稳定节点 |
| S06 | 混合故障 | 多故障并存 |
| S07 | 高峰调度 | 负载均衡 |
| S08 | Session 粘性 | 同会话同凭据 |
| S09 | 流式 + broken_stream | 流式正常 + 故障处理 |
| S10 | 长 Prompt | context_too_long 处理 |
| S11 | 配额恢复 | 窗口恢复生效 |
| S12 | 综合场景 | 15 模型 × 150 客户端 |
| S13 | 全部故障 | 预期 100% 失败 |
| S14 | 模型不存在 | 预期 100% 失败 |
| S15 | 跨组故障转移 | 跨组切换 |
| S16 | 配额快速恢复 | 快速恢复生效 |
| **S17** | **流式断连续传** | **pending 完整保存** |
| **S18** | **NULL 数据处理** | **容错不 panic** |
| **S19** | **租户隔离** | **跨租户访问失败** |

### 路由测试覆盖（52 个）

| 层级 | 模块 | 用例数 | 覆盖内容 |
|------|------|--------|---------|
| **Layer 1** | OpenAI Direct | 6 | 连通性、流式、工具调用 |
| **Layer 1** | Anthropic Direct | 6 | Messages API、工具使用 |
| **Layer 1** | Protocol Conversion | 6 | 双向协议转换 |
| **Layer 2** | Sticky Session | 6 | L1/L2/L3 粘性 |
| **Layer 2** | Compression | 6 | 上下文压缩 |
| **Layer 2** | Detection | 7 | 安全检测、PII |
| **Layer 3** | Full Routing | 7 | 完整路由逻辑 |
| **Layer 3** | Fault Recovery | 5 | 故障恢复 |
| **Layer 3** | Performance | 3 | 性能稳定性 |

---

## 🎯 推荐测试策略

### 开发阶段

```bash
# 每次代码修改后
cd tests/routing
export SKIP_LONG_TESTS=true
./run_all_tests.sh all  # 10 分钟快速验证
```

### PR/MR 阶段

```bash
# 完整路由测试
cd tests/routing
./run_all_tests.sh all  # 15-20 分钟

# 关键场景验证
cd ../../docs/全方面测试/scenarios
./S01_baseline.sh      # 基准
./S06_mixed_fault.sh   # 混合故障
./S08_sticky.sh        # 粘性
```

### 发布前验证

```bash
# 1. 完整路由测试
cd tests/routing
./run_all_tests.sh all | tee routing_report.log

# 2. 完整场景测试
cd ../../docs/全方面测试/scenarios
./run_all.sh --gateway http://localhost:8781 | tee scenario_report.log

# 3. 检查报告
cat routing_report.log | grep "Success Rate"
cat docs/全方面测试/results/REPORT.md
```

### 生产监控

```bash
# 定期运行（每小时/每天）
cd tests/routing
export SKIP_LONG_TESTS=true
./run_all_tests.sh all | tee production_health_$(date +%Y%m%d_%H%M).log
```

---

## 📚 详细文档索引

### 场景测试文档

1. [00-总览.md](./00-总览.md) - 场景测试执行入口
2. [01-测试架构设计.md](./01-测试架构设计.md) - 架构图、数据流
3. [02-测试环境部署.md](./02-测试环境部署.md) - 环境配置
4. [03-测试场景定义.md](./03-测试场景定义.md) - S1-S16 详细定义
5. [04-数据准备方案.md](./04-数据准备方案.md) - 数据 schema
6. [05-执行流程.md](./05-执行流程.md) - 执行步骤
7. [06-验收标准.md](./06-验收标准.md) - 通过标准
8. [07-故障类型矩阵.md](./07-故障类型矩阵.md) - 故障模式
9. [08-kill-switch.md](./08-kill-switch.md) - 模块旁路
10. [09-多模态与模型契约场景.md](./09-多模态与模型契约场景.md)
11. [10-多模态客户端与画像.md](./10-多模态客户端与画像.md)

### 路由测试文档

1. [routing-test-overview.md](./routing-test-overview.md) - 路由测试总览
2. [routing-test-guide.md](./routing-test-guide.md) - 详细使用指南
3. [routing-architecture.md](./routing-architecture.md) - 架构与流程图
4. [routing-diagnosis.md](./routing-diagnosis.md) - 根因诊断
5. [routing-test-cases.md](./routing-test-cases.md) - 用例清单
6. [routing-ci-cd.md](./routing-ci-cd.md) - CI/CD 集成

或直接查看原始文档：
- [诊断报告](../superpowers/specs/2026-07-18-routing-diagnosis.md)
- [测试策略](../superpowers/specs/2026-07-18-routing-layered-testing.md)
- [架构图表](../superpowers/specs/2026-07-18-routing-architecture-diagrams.md)
- [100% 覆盖](../superpowers/specs/2026-07-19-test-coverage-100-complete.md)
- [本地验证](../superpowers/specs/2026-07-19-test-framework-verification.md)

---

## 🔧 故障排查

### 场景测试问题

**Q: Mock suppliers 启动失败**
```bash
# 检查端口占用
lsof -i :17000-17060

# 重启
docs/全方面测试/tools/start_suppliers.sh restart
```

**Q: 场景测试超时**
```bash
# 使用快速模式
./run_all.sh --gateway http://localhost:8781 --fast
```

### 路由测试问题

**Q: Gateway 未运行**
```bash
curl http://localhost:8080/healthz
```

**Q: jq 未安装**
```bash
brew install jq  # macOS
sudo apt-get install jq  # Linux
```

**Q: 测试失败**
```bash
# 查看详细日志
./run_all_tests.sh layer1 2>&1 | tee debug.log
```

---

## 📈 持续集成

### GitHub Actions 示例

```yaml
name: Comprehensive Tests

on: [push, pull_request]

jobs:
  routing-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - name: Install deps
        run: sudo apt-get install -y jq curl
      - name: Start Gateway
        run: ./start_gateway.sh &
      - name: Run routing tests
        env:
          SKIP_LONG_TESTS: true
        run: |
          cd tests/routing
          ./run_all_tests.sh all
  
  scenario-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - name: Setup DB
        run: |
          psql -f docs/全方面测试/data/seed.sql
      - name: Start mock suppliers
        run: docs/全方面测试/tools/start_suppliers.sh
      - name: Run scenarios
        run: |
          cd docs/全方面测试/scenarios
          ./run_all.sh --gateway http://localhost:8781 --fast
```

---

## ✅ 验收标准总览

### 路由测试验收

- ✅ Layer 1: 18/18 通过 (100%)
- ✅ Layer 2: 19/19 通过 (100%)
- ✅ Layer 3: 15/15 通过 (100%)
- ✅ 总成功率: 100%
- ✅ 无 panic、无死锁

### 场景测试验收

- ✅ S01-S12: 成功率 ≥ 99%, P99 ≤ 2500ms
- ✅ S13-S14: 预期失败 (100% 错误率)
- ✅ S15-S16: 成功率 ≥ 99%
- ✅ 总请求: ~13,000+
- ✅ Gateway 稳定运行

---

## 🎓 最佳实践

### 日常开发

1. **小改动**: 只运行路由测试 Layer 1+2
2. **中等改动**: 运行完整路由测试
3. **大改动**: 路由测试 + 关键场景 (S01, S06, S12)

### 发布前

1. **完整路由测试**: 验证路由逻辑正确
2. **完整场景测试**: 验证业务场景
3. **性能基线对比**: 确保性能未劣化

### 生产监控

1. **定期健康检查**: 每小时运行路由测试
2. **告警触发**: 成功率 < 95% 告警
3. **趋势分析**: 追踪 P99 延迟趋势

---

## 📞 支持与联系

- **测试框架问题**: 查看 `tests/routing/README.md`
- **场景测试问题**: 查看 `docs/全方面测试/00-总览.md`
- **CI/CD 集成**: 查看 `routing-ci-cd.md`

---

**文档版本**: v2.0 (整合版)  
**最后更新**: 2026-07-19  
**维护者**: LLM Gateway Team
