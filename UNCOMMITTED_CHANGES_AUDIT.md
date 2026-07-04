# 未提交变更审计报告

**审计时间**: 2026-07-04
**审计目的**: 识别哪些变更应该保留/丢弃/恢复

---

## 一、变更清单

### 🚨 高危：MiniMax tool_call_id 修复被回退（必须恢复）

| 文件 | 变更类型 | 影响 | 建议 |
|---|---|---|---|
| `internal/ir/types.go` | ❌ 删除 `TargetProvider` 字段 | **生产故障** - 无法区分 provider 协议变体 | **必须恢复** |
| `internal/ir/serialize_anthropic.go` | ❌ 删除 `targetProvider` 参数 + MiniMax 分支逻辑 | **生产故障** - MiniMax 返回 tool_call_id not found (2013) | **必须恢复** |
| `domains/streaming/executors/executor_anthropic.go` | ❌ 删除 `irReq.TargetProvider = cand.CatalogCode` | **生产故障** - 无法传递 provider 信息 | **必须恢复** |
| `internal/ir/serialize_anthropic_minimax_test.go` | ❌ 删除测试文件 (179 行) | **测试覆盖率下降** | **必须恢复** |
| `domains/streaming/executors/executor_ir_test.go` | ✏️ 修改测试 | （需查看具体内容） | 待审计 |

**根因分析**：这些变更回退了 commit `b81c2a9c`（1214 行代码 + 3 个测试文件），该 commit 修复了 MiniMax tool_call_id 错误的 3 个串联 bug。

**影响评估**：
- ✅ commit `b81c2a9c` 已推送到 origin/main
- ❌ 当前工作区正在回退这些修复
- 🚨 如果提交当前变更 → **生产环境 MiniMax 请求全部失败**

**结论**：**必须丢弃这些变更，恢复到 HEAD 状态**

---

### ⚠️ 中危：Partition 查询优化被回退

| 文件 | 变更类型 | 影响 | 建议 |
|---|---|---|---|
| `db/migrations/339_fix_promote_batch_functions.sql` | ❌ 删除整个文件 (458 行) | **数据迁移失败** - promote 函数不存在 | **必须恢复** |
| `admin/provider_diagnose.go:481` | ❌ 从 `request_logs_default` 改回 `request_logs` | **查询性能下降** - 不必要的分区扫描 | **必须恢复** |
| `admin/providers.go:726` | ❌ 从 `request_logs_default` 改回 `request_logs` | **查询性能下降** | **必须恢复** |

**根因分析**：回退了 commit `c5b87be4`（467 行新增），该 commit 修复了：
1. 8 个 promote 函数的 PostgreSQL 语法错误（ORDER BY + LIMIT 在 DELETE RETURNING 中不支持）
2. 24h 窗口查询优化（使用 `*_default` 表而非父表）

**影响评估**：
- ✅ commit `c5b87be4` 已推送到 origin/main
- ❌ 当前工作区正在回退这些修复
- 🚨 如果提交当前变更 → bg/partition_manager.go 每小时调用 promote 函数时报 "function does not exist"

**结论**：**必须丢弃这些变更，恢复到 HEAD 状态**

---

### ✅ 低危：前端文案调整

| 文件 | 变更类型 | 影响 | 建议 |
|---|---|---|---|
| `web/src/components/ServiceLandingPage.vue` | ✏️ 修改默认文案 | **用户界面文本变化** | 可保留（如果是有意调整） |
| `web/src/views/LandingView.vue` | ✏️ 修改页面文案 | **用户界面文本变化** | 可保留（如果是有意调整） |

**变更内容**：
- "竞争优势" → "为什么选择 LLM Gateway"
- "开轩启圭WM · 企业智能工作台" → "LLM Gateway · 核心开源 · 中国本地化 · 私有部署"

**结论**：这是**唯一可能保留的变更**（前提是团队确认这是有意的品牌调整）

---

## 二、审计结论

### 🔴 必须丢弃的变更（8 个文件）

```bash
git restore admin/provider_diagnose.go
git restore admin/providers.go
git restore db/migrations/339_fix_promote_batch_functions.sql
git restore domains/streaming/executors/executor_anthropic.go
git restore domains/streaming/executors/executor_ir_test.go
git restore internal/ir/serialize_anthropic.go
git restore internal/ir/serialize_anthropic_minimax_test.go
git restore internal/ir/types.go
```

### 🟡 需要单独评估的变更（2 个文件）

```bash
# 如果前端文案调整是有意的，则：
git add web/src/components/ServiceLandingPage.vue
git add web/src/views/LandingView.vue
git commit -m "chore: 更新 Landing 页面品牌文案"

# 否则也丢弃：
git restore web/src/components/ServiceLandingPage.vue
git restore web/src/views/LandingView.vue
```

---

## 三、根本原因分析

**为什么会出现这些回退？**

可能的原因：
1. **误操作**：`git reset --hard HEAD~2` 然后 `git reset HEAD@{1}` 导致工作区残留
2. **Cherry-pick 冲突**：尝试 cherry-pick 旧 commit 导致覆盖
3. **Merge 冲突错误解决**：合并时选择了错误的版本
4. **IDE 缓存问题**：编辑器缓存了旧版本并自动保存

**如何避免未来再次发生？**

1. ✅ 使用 `git status` 和 `git diff` 仔细检查变更
2. ✅ 提交前运行测试（本项目已有 pre-commit hook）
3. ✅ 对于关键修复（如 MiniMax tool_call_id），添加回归测试防止误删
4. ✅ 使用 branch protection + PR review 流程

---

## 四、推荐执行计划

### Step 1: 恢复所有后端代码到 HEAD

```bash
git restore admin/provider_diagnose.go \
            admin/providers.go \
            db/migrations/339_fix_promote_batch_functions.sql \
            domains/streaming/executors/executor_anthropic.go \
            domains/streaming/executors/executor_ir_test.go \
            internal/ir/serialize_anthropic.go \
            internal/ir/serialize_anthropic_minimax_test.go \
            internal/ir/types.go
```

### Step 2: 确认前端文案是否保留

**如果保留前端文案**：
```bash
git add web/src/components/ServiceLandingPage.vue web/src/views/LandingView.vue
git commit -m "chore: 更新 Landing 页面品牌文案

- advantagesTitle: 竞争优势 → 为什么选择 LLM Gateway
- advantagesSubtitle: 差异化能力 → 面向有中国业务需求的全球企业
- footerText: 开轩启圭WM → LLM Gateway · 核心开源 · 中国本地化"
git push origin main
```

**如果丢弃前端文案**：
```bash
git restore web/src/components/ServiceLandingPage.vue web/src/views/LandingView.vue
```

### Step 3: 验证恢复

```bash
git status  # 应该显示 "nothing to commit, working tree clean"
git log --oneline -3  # 确认 HEAD 在 dccbbf69
```

### Step 4: 运行测试验证

```bash
go test ./internal/ir/... -v  # 验证 MiniMax 测试通过
go test ./domains/streaming/executors/... -v
```

---

**审计人**: AI Agent  
**审计日期**: 2026-07-04  
**紧急程度**: 🚨 P0 - 必须立即处理（MiniMax 生产故障）
