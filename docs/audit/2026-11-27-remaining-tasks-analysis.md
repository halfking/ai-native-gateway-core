# 剩余任务深度分析与执行方案

**生成时间**: 2026-11-27  
**分析范围**: 两周审计后续待办事项  
**状态**: 技术分析完成，待Owner决策执行

---

## 目录

1. [大分支合并分析](#1-大分支合并分析)
2. [远端分支清理方案](#2-远端分支清理方案)
3. [492投递方案技术分析](#3-492投递方案技术分析)
4. [生产环境版本差异](#4-生产环境版本差异)
5. [文档整合计划](#5-文档整合计划)
6. [执行优先级与时间线](#6-执行优先级与时间线)

---

## 1. 大分支合并分析

### 1.1 codex/request-metrics-v2

**基本信息**:
- 提交数: 74
- 落后main: 268笔
- Merge base: `40af488cc` (2026-11-14前后)
- 潜在冲突文件: **272个**

**冲突文件分布**:
```
后端 Go文件:        ~80个
  - admin/*.go      (21个，含bill_payment/channels/analytics_v2等)
  - bg/*.go         (15个，含billing_retry等)
  - domains/*.go    (30+个)
  - internal/*.go   (14+个)
  
前端文件:           ~180个
  - web/src/views/* (60+个Vue组件)
  - web/src/stores/* (状态管理)
  - web/src/locales/* (i18n，8语言×多文件)
  
迁移文件:          ~12个
  - sql/migrations/startup/* (1000-1002等)
  - installer相关
```

**核心冲突点**:
1. **计费系统重构** (1000-1002迁移已在main，但分支有不同版本)
2. **Admin API端点** (channels.go在分支新增336行，main已有不同修改)
3. **用量统计V2系统** (request_metrics相关，整个模块)
4. **前端dashboard重构** (大量Vue组件同时被两边修改)

**合并策略建议**:

**方案A: 分步cherry-pick (推荐)**
1. 分离独立功能模块（如纯前端UI改进）
2. 按功能域逐批cherry-pick:
   - Batch 1: 纯文档/测试 (~10提交)
   - Batch 2: 后端API（需解决冲突，~20提交）
   - Batch 3: 前端组件（需解决冲突，~30提交）
   - Batch 4: 迁移与schema（最复杂，~14提交）
3. 每批独立验证测试
4. 预估工作量: **3-5个工作日**

**方案B: 创建integration分支**
1. 从当前main创建`integration/request-metrics-v2`
2. 尝试合并，系统解决所有冲突
3. 分模块验证功能完整性
4. 合并回main
5. 预估工作量: **2-3个工作日**（风险更高）

**方案C: 废弃并重新实现 (不推荐)**
- 该分支有大量有效工作，废弃代价太高

**推荐**: 方案A，优先级设为**下周启动**

---

### 1.2 codex/s5-memora-route-contract

**基本信息**:
- 提交数: 5
- 落后main: 10笔
- Merge base: `53ff0abd6` (2026-11-26)
- 潜在冲突文件: **4个**

**冲突文件**:
```
domains/hooks/observability/telemetry/client.go       (已cherry-pick部分)
installer/cmd/llm-gw-installer/main.go                (迁移注册)
installer/internal/dbinit/runner.go                   (迁移链)
installer/internal/dbinit/runner_channel_ledger_test.go (测试)
```

**分支内容**:
1. **embeddings端点计量改进** (77行新增)
2. **memora路由契约** (contracts/memora-routes.json)
3. **用量账本服务路由结算** (已在Task 2中cherry-pick到main)

**合并策略建议**:

**方案: 直接合并 (推荐)**
- 冲突面很小（仅4个文件）
- 大部分冲突在installer相关文件（机械冲突）
- 预估工作量: **2-4小时**
- 建议: **本周内完成**

**执行步骤**:
```bash
git checkout -b merge/s5-memora-route-contract origin/main
git merge origin/codex/s5-memora-route-contract
# 解决4个文件冲突
# 1. telemetry/client.go - 已有部分在main，合并剩余
# 2-4. installer文件 - 迁移注册合并
go build ./...
go test ./domains/streaming/... ./installer/...
git commit -m "merge: s5-memora-route-contract"
```

---

## 2. 远端分支清理方案

### 2.1 已生成工具

**脚本位置**: `scripts/delete-merged-branches.sh`

**功能**:
- 自动验证分支合并状态（is-ancestor + patch-id）
- 预览模式（`--dry-run`）
- 安全执行模式（`--execute`，5秒确认期）

### 2.2 待删除分支（零丢失已验证）

| 分支名 | 验证方法 | 状态 |
|--------|----------|------|
| `fix/rm-turns-leg-savepoint` | is-ancestor | ✅ 是main祖先 |
| `codex/211-statistics-v2-release` | patch-id等价 | ✅ 两提交已cherry-pick |
| `feature/users-page-role-verify` | 文件内容对比 | ✅ 内容完全相同 |

### 2.3 执行步骤

```bash
# 1. 预览（推荐先执行）
bash scripts/delete-merged-branches.sh --dry-run

# 2. 实际删除（需Owner确认）
bash scripts/delete-merged-branches.sh --execute

# 3. 清理本地跟踪
git fetch --prune
git branch -d fix/rm-turns-leg-savepoint \
             codex/211-statistics-v2-release \
             feature/users-page-role-verify
```

**建议**: 本周内执行（优先级：中）

---

## 3. 492投递方案技术分析

### 3.1 问题描述

**表**: `customers` 和 `channel_users`  
**定义文件**: `sql/migrations/startup/492_channels_customers.sql`  
**问题**: 文件存在但**不在任何投递通道里**

**四条投递通道实测**:
1. ❌ 211部署通道 `scripts/apply-db-revision-sequence.sh` 的 `files=()`
2. ❌ 安装器清单 `sql/schema/installed_startup_migrations.tsv`
3. ❌ 安装器embeddata `installer/cmd/llm-gw-installer/embeddata/startup/`
4. ❌ 安装器迁移目录 `installer/migrations/`

**生产验证** (2026-11-26):
- 211 (`acc-pg17/llm_gateway`): `public.customers` 不存在
- 154: `public.schema_migrations` 无 '492' 行
- 实际影响: `admin/channels.go` 四处引用会 **42P01 (relation does not exist)**

### 3.2 为什么门没拦住

编号**492**远在当前最大号**999**的近邻窗口外 → 投递覆盖门只检查最大编号附近 → **492被漏检**

这是"用旧编号当新迁移"绕过门禁的典型案例。

### 3.3 技术方案对比

#### 方案A: 搬进投递通道（推荐）

**步骤**:
```bash
# 1. 安装器五点同步
cp sql/migrations/startup/492_channels_customers.sql \
   installer/cmd/llm-gw-installer/embeddata/startup/

# 2. 更新main.go的go:embed
# 3. 更新installed_startup_migrations.tsv
# 4. 更新runner.go的StartupFiles数组
# 5. 更新apply-db-revision-sequence.sh的files=()

# 6. 验证
go build ./installer/...
bash scripts/audit/build-fresh-install-db.sh  # 新装链验证
```

**优点**:
- ✅ 彻底解决（表会被创建）
- ✅ 符合现有投递架构
- ✅ 新环境/fresh安装自动获得

**缺点**:
- ⚠️ 需要schema变更（生产环境需执行迁移）
- ⚠️ 现有环境需手动补迁移或在下次部署时应用

#### 方案B: 修改读端（备选）

**步骤**:
```go
// admin/channels.go 四处读点改为：
// 方案B1: 条件查询（表存在才读）
if tableExists("customers") {
    rows, err = db.Query("SELECT ... FROM customers ...")
} else {
    return emptyResult, nil
}

// 方案B2: 完全移除channels相关端点（如果W4.3不需要）
```

**优点**:
- ✅ 零schema变更
- ✅ 不需要迁移

**缺点**:
- ❌ 功能缺失（channels端点不可用或返回空）
- ❌ 不符合W4.3需求（如果该功能是必需的）

### 3.4 推荐方案

**如果W4.3需要channels功能**: 方案A（搬进投递通道）  
**如果W4.3不需要**: 方案B2（移除端点）

**Owner决策要点**:
1. W4.3是否需要 `/api/channels/*` 端点？
2. 现有生产环境（211/154）是否能接受增量迁移？
3. 时间窗口：W4.3预计上线时间？

**建议**: 本周内决策，下周执行

---

## 4. 生产环境版本差异

### 4.1 当前状态 (2026-11-27)

| 环境 | 当前版本 | 落后笔数 | 相对SHA | 最后更新 |
|------|----------|----------|---------|----------|
| 211 | 未知 | 6笔 | `origin/main@7894727bcb` | 需实测 |
| 245 | 未知 | 86笔 | 同上 | 需实测 |
| 154 | 未知 | 88笔 | 同上 | 需实测 |

**注**: 落后数是快照量，会随main前进而增大

### 4.2 追平收益分析

**新增功能** (近期main新增):
- 计费系统 1000-1002 迁移（五点已同步）
- Telemetry服务路由结算修复
- Casdoor E2E测试基础设施
- 多项门禁收口和P0/P1修复

**风险**:
- 1000-1002迁移需要DDL执行（有锁时间）
- 492未投递可能导致channels端点失败（取决于是否调用）

### 4.3 推荐策略

**211**: 
- 优先级: 中（落后较少）
- 建议: 本月内追平
- 前置条件: 492方案决策

**245/154**:
- 优先级: 低（落后较多但稳定）
- 建议: 分批追平，每次10-20笔
- 时间线: 1-2个月

---

## 5. 文档整合计划

### 5.1 重复文档识别

**12小时审计报告** (5份):
```
docs/12小时内修订审计-20261115-1300.md
docs/12小时内修订审计-2026-11-16-0804.md
docs/12小时内修订审计-2026-11-20-0400.md
docs/12小时内修订审计-2026-11-20-0800.md
docs/12小时内修订审计-2026-11-24-0704.md
```

**211部署系列** (3份):
```
docs/211-deployment-verification-plan.md
docs/2026-11-19-211部署验证状态报告.md
docs/211-user-auth-final-verification.md
```

### 5.2 整合方案

#### 阶段1: 建立索引页 (1小时)

创建 `docs/audit/index.md`:
```markdown
# 审计报告索引

## 12小时审计系列
- [2026-11-15] 第一轮...
- [2026-11-16] 第二轮...
...

## 专项审计
- 211部署验证系列
- 统计系统标准化
...
```

#### 阶段2: 归档历史文档 (2小时)

```bash
mkdir -p docs/audit/archive/{12h-audits,211-deployment,statistics}
mv docs/12小时内修订审计-*.md docs/audit/archive/12h-audits/
mv docs/211-*.md docs/audit/archive/211-deployment/
```

#### 阶段3: 创建权威文档 (根据需要)

对于频繁引用的主题（如统计系统），合并为单一权威文档+版本历史。

**执行优先级**: 低（不阻塞其他工作）

---

## 6. 执行优先级与时间线

### 6.1 本周内 (2026-11-27 ~ 12-01)

| 任务 | 优先级 | 预估工时 | Owner决策需求 |
|------|--------|----------|----------------|
| 492投递方案决策 | 🔴 P0 | - | ✅ 需要 |
| s5-memora分支合并 | 🟡 P1 | 2-4h | ❌ 不需要 |
| 远端分支删除 | 🟡 P1 | 0.5h | ✅ 需要（确认） |

### 6.2 下周 (2026-12-02 ~ 12-08)

| 任务 | 优先级 | 预估工时 | 依赖 |
|------|--------|----------|------|
| request-metrics-v2合并启动 | 🟡 P1 | 3-5天 | 492决策完成 |
| 211生产追平 | 🟢 P2 | 1-2h | 492执行完成 |

### 6.3 本月内 (12月)

| 任务 | 优先级 | 预估工时 |
|------|--------|----------|
| 245/154分批追平 | 🟢 P2 | 分批进行 |
| 文档整合 | 🟢 P3 | 3-4h |

---

## 7. 自动化执行摘要

### 7.1 已生成工具

✅ `scripts/delete-merged-branches.sh` - 分支删除工具  
✅ 冲突分析脚本（临时，/tmp/analyze-branch-conflicts.sh）

### 7.2 已完成分析

✅ request-metrics-v2冲突面（272文件）  
✅ s5-memora-route-contract冲突面（4文件）  
✅ 492投递方案技术细节  
✅ 生产环境版本差异框架

### 7.3 待Owner决策（阻塞项）

🔴 **492投递方案**: 搬通道 vs 移除端点  
🟡 **远端分支删除**: 确认执行3个分支删除  
🟡 **request-metrics-v2**: 选择合并方案（A/B）

---

## 8. 下一步行动

### 立即可执行（无需等待）

```bash
# 1. s5-memora-route-contract合并
git checkout -b merge/s5-memora origin/main
git merge origin/codex/s5-memora-route-contract
# 解决冲突后
git push origin merge/s5-memora

# 2. 远端分支删除预览
bash scripts/delete-merged-branches.sh --dry-run
```

### 需Owner决策后执行

1. **492方案** → 准备迁移脚本或端点移除PR
2. **分支删除** → 执行delete-merged-branches.sh --execute
3. **request-metrics-v2** → 按选定方案启动合并

---

**报告完成时间**: 2026-11-27 15:00  
**有效期**: 本报告基于当前main状态，建议1周内执行关键决策
