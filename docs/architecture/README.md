# LLM Gateway Go 领域驱动架构重构

> **状态**: ✅ Design Completed  
> **日期**: 2026-06-25  
> **团队**: AI Architecture Team  

## 📋 文档导航

| 文档 | 描述 | 状态 |
|------|------|------|
| [domain-refactoring-plan.md](./domain-refactoring-plan.md) | 完整架构设计方案 | ✅ 已完成 |
| [implementation-plan.md](./implementation-plan.md) | 详细实施计划 | ✅ 已完成 |

---

## 🎯 重构目标

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
- [现有 Hook 示例](../../relay/metatool_interceptor.go)
- [现有压缩器](../../compressor/session_compressor.go)

---

**最后更新**: 2026-06-25  
**维护者**: AI Architecture Team  
**版本**: v2.0
