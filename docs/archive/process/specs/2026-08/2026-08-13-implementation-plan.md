# 三层缓存 + 压缩质量评分 + 敏感信息脱敏 实施计划

> **状态**：实施中
> **日期**：2026-08-13
> **预计完成**：2026-08-27（分三个阶段）

---

## 实施阶段规划

### Phase 1：P0 关键修复（2天，2026-08-13 ~ 2026-08-14）

**目标**：修复最关键的安全和质量问题

- [x] Task 1.1: 增强占位符验证机制
  - [ ] 在 `SanitizeRestoreInterceptor` 添加 `validatePlaceholders` 方法
  - [ ] 添加 Prometheus 指标 `sanitize_placeholder_tampering_total`
  - [ ] 记录详细告警日志

- [x] Task 1.2: System Prompt 占位符保护指令
  - [ ] 在 `transformation/to_openai.go` 注入保护指令
  - [ ] 在 `transformation/to_anthropic.go` 注入保护指令
  - [ ] 添加功能开关 `SANITIZE_SYSTEM_PROMPT_ENABLED`

- [x] Task 1.3: 压缩质量日志初版
  - [ ] 定义 `CompressionQualityScore` 结构
  - [ ] 在 `session_compressor.go` 添加 `logCompressionQuality` 方法
  - [ ] 计算基础指标（token 节省、消息保留率）

**验收标准**：
- [ ] LLM 改写占位符时有告警日志
- [ ] System Prompt 注入后 LLM 保留占位符
- [ ] 压缩质量日志包含 token_savings_pct / semantic_fidelity

---

### Phase 2：P1 三层缓存对齐（1周，2026-08-15 ~ 2026-08-21）

**目标**：实现三层独立存储语义

- [ ] Task 2.1: 扩展 SessionState 结构
  - [ ] 添加 `RawMessages []Message` 字段（L1 原始）
  - [ ] 添加 `CompressedMessages []Message` 字段（L2 压缩）
  - [ ] 添加 `AuditedMessages []Message` 字段（L3 审核）
  - [ ] 添加 `SanitizeMapRef string` 字段
  - [ ] 添加 `SanitizeStats` 结构
  - [ ] 添加 `CompressionQuality` 字段

- [ ] Task 2.2: 修改脱敏流程
  - [ ] 在 `SessionCompressor.Prepare` 开始前脱敏
  - [ ] L1 存储真实值（RawMessages）
  - [ ] 生成映射表并存入 Redis
  - [ ] 更新 `SanitizeStats`

- [ ] Task 2.3: 修改压缩流程
  - [ ] 压缩后存储占位符消息（CompressedMessages）
  - [ ] 构建完整 AlignmentMap
  - [ ] 计算压缩质量评分

- [ ] Task 2.4: 修改审核流程
  - [ ] 审核后存储 AuditedMessages
  - [ ] 关联 SanitizeMap
  - [ ] 计算审核分数

**验收标准**：
- [ ] `SessionState` 可以区分 L1/L2/L3 三层消息
- [ ] L1 存储真实值，L2 存储占位符
- [ ] 映射表与 SessionState 生命周期同步

---

### Phase 3：P2 增强功能（1周，2026-08-22 ~ 2026-08-27）

**目标**：可选增强和优化

- [ ] Task 3.1: 占位符签名（可选，高安全场景）
  - [ ] 实现 `GenerateSignedPlaceholder`
  - [ ] 实现 `ValidateSignedPlaceholder`
  - [ ] 添加功能开关 `SANITIZE_SIGNED_PLACEHOLDER`

- [ ] Task 3.2: 压缩质量完整评分
  - [ ] 实现 `computeInformationDensity`
  - [ ] 实现 `calculateMessageDensity`
  - [ ] 实现语义保真度评分
  - [ ] 综合评分算法

- [ ] Task 3.3: 审计日志
  - [ ] 记录所有脱敏/还原操作
  - [ ] 支持按 session 查询脱敏历史
  - [ ] 导出到 PostgreSQL `audit_logs` 表

- [ ] Task 3.4: 文档和测试
  - [ ] 更新 `security/sanitize/README.md`
  - [ ] 添加集成测试
  - [ ] 性能基准测试

**验收标准**：
- [ ] 压缩质量评分完整（信息密度 + 语义保真度 + 综合评分）
- [ ] 审计日志可追溯
- [ ] 测试覆盖率 > 80%

---

## 当前进度

**Phase 1 - 已完成** ✅

开始时间：2026-08-13 14:30
完成时间：2026-08-13 16:45
Commit: 1888e4114

**Phase 2 - 进行中** 🚧

开始时间：2026-08-13 16:50
预计完成：2026-08-13 20:00

---

## 风险和依赖

| 风险 | 缓解措施 |
|------|---------|
| SessionState 结构变更可能影响现有缓存 | 保持向后兼容，新字段都用 `omitempty` |
| L1 存储真实值可能有安全风险 | L1 仅进程内缓存，不出网关 |
| 压缩质量评分计算可能影响性能 | 异步计算，不阻塞主流程 |
| System Prompt 注入可能影响 LLM 行为 | 添加功能开关，可随时关闭 |

---

## 回滚预案

每个 Phase 独立 commit，可独立回滚：

```bash
# Phase 1 回滚
git revert <phase1-commit-hash>

# Phase 2 回滚
git revert <phase2-commit-hash>

# Phase 3 回滚
git revert <phase3-commit-hash>
```
