# request-metrics-v2 大分支合并准备文档

> 生成时间：2026-11-27 19:30  
> 分支状态：74提交，272文件冲突  
> 推荐方案：分步cherry-pick（Batch 1-4）  
> 预估工时：3-5个工作日

---

## 📊 分支状态概览

### 基本信息

| 项目 | 数据 |
|------|------|
| 分支名 | codex/request-metrics-v2 |
| 提交数 | 74个 |
| 落后main | 268笔 |
| 冲突文件 | 272个 |
| 创建时间 | 2026年初 |
| 最后更新 | 数月前 |

### 冲突面分析

| 类别 | 文件数 | 占比 | 复杂度 |
|------|--------|------|--------|
| 后端代码 | ~80 | 29% | 高 |
| 前端组件 | ~180 | 66% | 中 |
| 迁移文件 | ~12 | 5% | 高 |

### 风险评估

🔴 **高风险区域**：
- 迁移文件：可能与已应用迁移冲突
- 核心API：request_logs/request_metrics表操作
- 权限逻辑：admin handler权限门禁

🟡 **中风险区域**：
- 前端组件：Vue组件大量冲突
- 路由配置：可能与新增路由冲突
- 类型定义：TypeScript接口变更

🟢 **低风险区域**：
- 文档：纯文档变更
- 测试：独立测试文件

---

## 🎯 推荐方案：分步Cherry-pick

### 方案概述

**核心思路**：按依赖关系和风险级别分4个batch，逐批合并

**优势**：
- 每批独立验证，降低风险
- 冲突集中处理，逐步收敛
- 可暂停/回滚，不影响main稳定性
- 便于code review

**时间线**：
- Batch 1: 1天（文档/测试）
- Batch 2: 1-2天（后端API）
- Batch 3: 1-2天（前端组件）
- Batch 4: 1天（迁移+收尾）
- 总计: 3-5个工作日

---

## 📦 Batch 1: 文档与测试（优先级：P2）

### 目标

建立基础，无破坏性变更

### 包含内容

**文档类**（预估10个提交）：
- request-metrics功能设计文档
- API文档更新
- README更新
- changelog条目

**测试类**（预估10个提交）：
- 独立测试文件
- 测试工具函数
- Mock数据

### 执行步骤

```bash
# 1. 创建工作分支
git checkout main
git pull origin main
git checkout -b merge/request-metrics-v2-batch1

# 2. 分析文档/测试提交
git log codex/request-metrics-v2 --oneline --grep="doc\|test\|spec" > batch1-candidates.txt

# 3. 逐个cherry-pick
git cherry-pick <commit-sha>
# 解决冲突后：
git add .
git cherry-pick --continue

# 4. 验证
go test ./...
git diff main --stat

# 5. 推送review
git push origin merge/request-metrics-v2-batch1
# 创建PR，标题：[Batch 1/4] request-metrics-v2: docs & tests
```

### 验证清单

- [ ] 编译通过
- [ ] 现有测试不受影响
- [ ] 新增测试可运行
- [ ] 文档无冲突

### 预估时间

**1个工作日**（包含review时间）

---

## 🔧 Batch 2: 后端API（优先级：P0）

### 目标

合并核心后端逻辑

### 包含内容

**后端文件**（预估20个提交）：
- `domains/metrics/` 新模块
- `admin/*_metrics_*.go` handler
- `internal/*/metrics*.go` 内部逻辑
- API路由注册

### 执行步骤

```bash
# 1. 创建工作分支（基于batch1或main）
git checkout main  # 或 merge/request-metrics-v2-batch1（如batch1已合并）
git checkout -b merge/request-metrics-v2-batch2

# 2. 分析后端提交
git log codex/request-metrics-v2 --oneline -- '*.go' ':!*_test.go' ':!web/' > batch2-candidates.txt

# 3. 按依赖顺序cherry-pick
# 顺序：数据模型 → 业务逻辑 → handler → 路由
git cherry-pick <commit-sha>

# 4. 解决冲突策略
# - 保留main的权限逻辑（如有菜单重构改动）
# - metrics逻辑以分支为准
# - import冲突：合并两边

# 5. 验证
go build ./...
go test ./domains/metrics/...
go test ./admin/...

# 6. 推送review
git push origin merge/request-metrics-v2-batch2
```

### 冲突处理指南

#### admin/handler冲突

**场景**：权限中间件冲突

**策略**：
```go
// 保留main的最新权限逻辑
// 添加metrics端点
func (h *Handler) RegisterRoutes() {
    // ... main已有路由 ...
    
    // 新增metrics路由（从分支）
    r.Get("/api/admin/metrics/requests", h.requestMetricsHandler)
}
```

#### import冲突

**场景**：新增import与main冲突

**策略**：合并两边import，按字母序排列

#### 类型定义冲突

**场景**：struct字段冲突

**策略**：合并字段，确保JSON tag不冲突

### 验证清单

- [ ] 编译通过（无42P01/42703错误）
- [ ] 现有API不受影响
- [ ] 新增API可访问
- [ ] 权限门禁正确
- [ ] 测试通过

### 预估时间

**1-2个工作日**（冲突较多）

---

## 🎨 Batch 3: 前端组件（优先级：P1）

### 目标

集成前端UI

### 包含内容

**前端文件**（预估30个提交）：
- `web/src/views/*Metrics*.vue` 组件
- `web/src/components/metrics/` 模块
- `web/src/stores/metrics*.ts` 状态管理
- `web/src/api/metrics.ts` API调用
- 路由配置
- 多语言文件

### 执行步骤

```bash
# 1. 创建工作分支（基于batch2）
git checkout merge/request-metrics-v2-batch2  # 或main（如batch2已合并）
git checkout -b merge/request-metrics-v2-batch3

# 2. 分析前端提交
git log codex/request-metrics-v2 --oneline -- 'web/' > batch3-candidates.txt

# 3. 按依赖顺序cherry-pick
# 顺序：API层 → store → 组件 → 路由 → i18n
git cherry-pick <commit-sha>

# 4. 解决冲突策略
# - 路由：合并两边路由定义
# - menu-config.json：合并菜单项
# - i18n：合并翻译键值

# 5. 验证
cd web
npm install  # 如有新依赖
npm run type-check
npm run test
npm run build

# 6. 推送review
git push origin merge/request-metrics-v2-batch3
```

### 冲突处理指南

#### router.ts冲突

**场景**：路由定义冲突

**策略**：
```typescript
// 合并两边路由
const routes = [
  // ... main已有路由 ...
  
  // 新增metrics路由（从分支）
  {
    path: '/admin/metrics',
    component: () => import('./views/admin/MetricsView.vue'),
    meta: { requiresAuth: true, role: 'admin' }
  }
]
```

#### menu-config.json冲突

**场景**：菜单项冲突

**策略**：合并JSON，确保ID不冲突

#### locales冲突

**场景**：多语言文件冲突

**策略**：合并翻译键，保留main的已有翻译

### 验证清单

- [ ] TypeScript类型检查通过
- [ ] 前端测试通过
- [ ] 构建成功
- [ ] 新增页面可访问
- [ ] 菜单显示正常
- [ ] 多语言完整

### 预估时间

**1-2个工作日**（冲突量大但复杂度中等）

---

## 🗄️ Batch 4: 迁移与收尾（优先级：P0）

### 目标

完成数据库迁移和最终集成

### 包含内容

**迁移文件**（预估14个提交）：
- `sql/migrations/startup/9xx_request_metrics*.sql`
- `sql/migrations/domain/xxx_*.sql`
- 迁移配置更新

**收尾工作**：
- 配置文件更新
- 依赖版本同步
- 最终集成测试

### 执行步骤

```bash
# 1. 创建工作分支（基于batch3）
git checkout merge/request-metrics-v2-batch3  # 或main
git checkout -b merge/request-metrics-v2-batch4

# 2. 分析迁移提交
git log codex/request-metrics-v2 --oneline -- 'sql/migrations/' > batch4-candidates.txt

# 3. ⚠️ 关键：检查迁移号冲突
ls sql/migrations/startup/ | grep "^9[0-9][0-9]_"
# 如983已存在（request_metrics），需要协调

# 4. Cherry-pick迁移
git cherry-pick <commit-sha>

# 5. ⚠️ 冲突解决：迁移号重命名
# 如分支的983与main的983冲突：
# 方案A：分支983改为983d（字母后缀）
# 方案B：分支983改为更高号（如1003）

# 6. 五点同步（如新增startup迁移）
# - embeddata副本
# - go:embed
# - StartupFiles
# - TSV清单
# - apply脚本

# 7. 验证
go test ./installer/internal/dbinit/ -run TestStartupManifest
go build ./installer/cmd/llm-gw-installer/

# 8. 推送review
git push origin merge/request-metrics-v2-batch4
```

### 迁移号冲突处理

**场景1**：983已被main使用

**解决**：
- 分支983改为983e（字母后缀策略）
- 更新所有引用该迁移的代码
- 更新注释说明改号原因

**场景2**：迁移内容与已应用迁移冲突

**解决**：
- 分析diff，提取未应用的DDL
- 创建新增量迁移（号码更高）
- 废弃分支原迁移

### 验证清单

- [ ] 迁移号无冲突
- [ ] 五点同步完整（如适用）
- [ ] TestStartupManifest通过
- [ ] installer编译通过
- [ ] 完整集成测试通过
- [ ] 文档更新完整

### 预估时间

**1个工作日**（迁移需谨慎）

---

## 🔍 完整集成验证

### 所有batch合并后执行

```bash
# 1. 合并所有batch到集成分支
git checkout main
git checkout -b merge/request-metrics-v2-integration
git merge merge/request-metrics-v2-batch1
git merge merge/request-metrics-v2-batch2
git merge merge/request-metrics-v2-batch3
git merge merge/request-metrics-v2-batch4

# 2. 完整编译
go build ./...

# 3. 完整测试
go test ./...

# 4. 前端测试
cd web
npm run test
npm run build

# 5. E2E测试（如有）
cd ../localtest
./run-e2e.sh

# 6. 最终review后合并到main
git checkout main
git merge merge/request-metrics-v2-integration
git push origin main
```

### 验证清单

- [ ] 编译通过
- [ ] 所有测试通过
- [ ] 前端构建成功
- [ ] E2E测试通过
- [ ] 文档完整
- [ ] 迁移verified

---

## ⏱️ 时间线规划

### 建议执行时间表

| 阶段 | 时间 | 交付物 | 评审人 |
|------|------|--------|--------|
| **准备阶段** | 0.5天 | 本文档+冲突预分析 | - |
| **Batch 1** | 1天 | PR#1 (docs/tests) | 1人 |
| **Batch 2** | 1-2天 | PR#2 (backend) | 2人 |
| **Batch 3** | 1-2天 | PR#3 (frontend) | 1人 |
| **Batch 4** | 1天 | PR#4 (migrations) | 2人 |
| **集成验证** | 0.5天 | 完整测试报告 | - |
| **总计** | **4-6天** | 4个PR + 集成分支 | - |

### 人员安排建议

**主执行人**：1人全程负责  
**Review支持**：2人轮换review  
**专项支持**：
- 迁移专家（Batch 4）
- 前端专家（Batch 3）

---

## 🚨 风险与应对

### 风险1：迁移号冲突

**概率**：高  
**影响**：阻塞Batch 4

**应对**：
- 提前检查main的980-999迁移
- 准备改号方案（字母后缀）
- 预留983d/983e等编号

### 风险2：权限逻辑冲突

**概率**：中  
**影响**：Batch 2受阻

**应对**：
- 优先保留main的权限逻辑
- metrics端点适配main的中间件
- 增量添加，不替换

### 风险3：前端构建失败

**概率**：中  
**影响**：Batch 3受阻

**应对**：
- 提前npm install检查依赖
- 类型错误逐个修复
- 保留main的构建配置

### 风险4：测试回归失败

**概率**：中  
**影响**：影响上线

**应对**：
- 每个batch独立测试
- 集成阶段完整回归
- 预留修复时间（+1天）

---

## 📋 执行Checklist

### 开始前

- [ ] 本文档review完成
- [ ] 时间窗口已预留（4-6天）
- [ ] Review人员已确认
- [ ] main分支最新（git pull）
- [ ] 工作区干净（git status）

### Batch 1

- [ ] 分支创建
- [ ] 提交cherry-pick
- [ ] 冲突解决
- [ ] 测试验证
- [ ] PR创建
- [ ] Code review
- [ ] 合并到main

### Batch 2

- [ ] （同Batch 1流程）
- [ ] 特别注意：权限冲突处理

### Batch 3

- [ ] （同Batch 1流程）
- [ ] 特别注意：前端构建验证

### Batch 4

- [ ] （同Batch 1流程）
- [ ] 特别注意：迁移号检查
- [ ] 特别注意：五点同步（如需要）

### 集成验证

- [ ] 集成分支创建
- [ ] 完整编译
- [ ] 完整测试
- [ ] E2E测试
- [ ] 文档更新
- [ ] 最终合并

---

## 📚 参考资源

- 冲突分析：`docs/audit/2026-11-27-remaining-tasks-analysis.md` 第1章
- 五点同步规范：`installer/internal/dbinit/runner.go` 注释
- 迁移号规范：`sql/migrations/startup/` 头部说明

---

**文档维护者**: 项目审计团队  
**最后更新**: 2026-11-27  
**下次更新**: 执行过程中根据实际情况更新

---

## 📌 执行进度登记（2026-11-27 逐笔对拍后）

> 本节为执行修正记录：逐笔对拍（git cherry patch-id + 逐文件 blob SHA）后，原计划的预估搬运面大幅缩小。**计划文档的笔数预估是在未对拍时写的；实际结论以下节为准。**

### Batch 1（文档/测试）— ✅ 已完成

- 原计划 ~20 笔 ⇒ **实际净缺口仅 3 个文件**：20 笔 docs/test 提交逐文件对拍后几乎全部已在 main（验收文档由保全提交 `9a0999515` 收编、blob SHA 逐字相同；AGENTS 恢复提交照搬会撞归档重复门；gate 台账引用 main 不存在的 routes 文件）。
- 已交付：3 笔摘录提交推 `merge/request-metrics-v2-batch1`（真基线 fc6f7f6a7，tip 475c246b2），与源分支字节一致；go build EXIT=0 + AGENTS/ledger 门 16 pass（隔离 worktree）。
- PR：材料备齐于 `docs/audit/batch1-pr-description.md`；codeup API 无凭据（aliyun OpenAPI 需 AccessKey），浏览器代建走到 RAM 密码页被 MFA 拦住（克隆密码≠RAM 登录密码）⇒ **需用户登录后一键创建**。

### Batch 2（后端 API）— ✅ 已完成（对拍 + 摘录 + PR）

对全部 74 笔未并提交逐一取证后（55 非 merge + 19 merge），结论：

| 归类 | 笔数 | 说明 |
|---|---|---|
| merge 空笔 | 19 | 全部是 main 流入分支，零分支内容 |
| Batch 1 已处理 | 20 | 见上 |
| 被 main 收编 | 1 | bcb216dad：其 admin V2 面与 main 逐 blob 相同（11dc125c4 收编、路由改内联）；其 983d 独立表方案**被分支自己的 8928e745e 删光**（⚠️ 勿整体搬运） |
| 分支内自取代 | 1 | 8928e745e：973 与 main 同 blob（分支已向 main 基座对齐），983j 视图/repair_state 是分支方案本体 |
| main 异形等价覆盖 | 2 | 5379c7296（TS2532 可选链 vs hits[0].l）、d3efb2821（main 自己已删漂移行号引用） |
| 伪修复/不适用 | 2 | b963fd026（撞名对象是分支自己的夹具）、8909da073（守卫对象在 main 不存在——main 用 fact_id 游标） |
| **互斥不可搬** | 24 | 全部 perf 提交锚在 branch-only 文件（`request_daily_board.go`/`daily_history.go`/`projected_workbook.go` 等 11 个），读「视图+日物化」面，与 main「基表直查+fact_id 游标」互斥 |
| **净缺口可搬** | **1 笔的部分** | **1e510cf56 的 4 个文件**：`internal/relrefs/product.go`（CTE 解析器两修复：`^` 锚 + 全名匹配）＋ `selftest.go` 夹具 ＋ `dangling_relation_gate_test.go`（修后可摘 'static' 豁免）＋ `migration_745_test.go`（改用 deploy-surface.sh 提取）。零分支表方案耦合、零 schema。TSV/locale 部分留在分支 |

**关键架构事实**：分支最终基座 = main 的 973（同 blob c75451ba，两线在 973 已合流）；分叉在读面——分支叠 983j 视图 + 983k–983r 日物化族（读侧加速），main 走基表直查 + 1000-1002 计费线（已验证 1000-1002 与投影读面零表级交集）。若 owner 未来想要分支的读侧加速，那是「按 main 棘轮重新立项」的整套新决策（8 迁移 + 11 Go 文件 + web 组件），不是搬运。

**交付**：4 文件摘录为 1 笔提交（`53166a9bd`）推 `merge/request-metrics-v2-batch2`（基线 = 当时的 codeup/main dfdf464c2），与源提交 blob 逐字节一致。验证全绿：TestMigration745 ok、dangling 门家族 ok（摘豁免后仍绿）、`--self-test` SELF-TEST PASS、go vet EXIT=0。**PR #2 已创建**：https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go/change/2（无代码冲突，待合并）。

**PR #1（Batch 1）也已创建**：https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go/change/1（无代码冲突——用户钉钉扫码登录后由浏览器代建）。

### ✅ 两 PR 合并终态（2026-11-28 凌晨核验）

- **PR #1 内容 100% 进 main**：并行车把 `merge/request-metrics-v2-batch1` 分支整体合入（含 Batch 1 三笔 4dedc8938/305de6639/475c246b2 及其续推的 merge 笔），git ancestry 实证（web 页状态滞后，以 git 为准）。
- **PR #2 已正式合并**：merge commit `8f6ad6c0d`（浏览器代点：合并节点方式 + 提交确认），合并后 main 的 4 文件 blob 终验 = PR 版本（relrefs `^` 锚实证在位），源分支保留。
- 合并后 main 尖端 `8f6ad6c0d`；211 现役 7b938ced@3321 相对其落后 28 笔（并行车 plugins 灰度系列为主，**无迁移增量**）⇒ 211 无需紧急再追平，与 245/154 轮一并处理。

### Batch 3（前端）— 对拍完成，实际可搬面 ≈ 0 个文件

163 个分支侧改动文件按文件聚合对拍（tip-vs-tip blob）：

| 分类 | 数量 | 说明 |
|---|---|---|
| main 已逐字节收编 | **96** | 全部 V2 主力视图（board 全家/SessionAnalytics/UserProfile/13 语言 locales 等），由 11dc125c4 + 9a0999515 收编 |
| main 已更新（旧形态差异） | **57** | 方向一致：main 在 fork 后继续演进。典型：`api/usage.ts` main 已删死代码 `getDashboardOverview`（搬回=回归）；`SaCostCell` main 是双单位禁相加口径 vs branch 锚定 branch-only `usd_evidence_requests` 字段（main 后端零命中） |
| branch-only 新文件 | **3** | `ReportDayDetails.vue/.test.ts`、`api/reportrollup.test.ts`——**全部悬空**：依赖 main 不存在的 `/api/admin/report-rollup/v2/daily` 端点（main 白名单只有 summary/dimensions/export）、`BuildProjectedWorkbookReport`、`downloadSameOriginFile` |
| 净新增文档 | **7** | 7 语言 page-help 描述「Expand By day → Daily details」UI——该 UI 只在分支存在，描述 main 上没有的功能 |

### Batch 4（迁移）— 对拍完成：983j–r 技术可搬、语义不可先行

- 分支迁移面（973 embeddata 副本 + 983j–r 九件 × 五点全链）**自洽自包含，与 main 已应用迁移（973/983/1000-1002/492/999b-d）零表级/列级冲突**（983j 视图是 pg_attribute 动态生成、无硬编码列；触发器名无碰撞；三处字节 parity 9/9 EQUAL 实证）。
- 但**单独搬运无收益**：① 九张物化表的全部 Go 读端在 Batch 2 判定互斥不可搬的 24 笔 perf 提交里——搬 SQL 不搬 Go = 纯死表 + 白付触发器失效成本；② TSV/manifest **正面撞号**（分支 408-416 vs main 408-415 = 999b-d/633/1000-1002/492），合并须重排五点全链并重跑 fresh 建造器验证。

### 🎯 全战役最终结论（四批对拍闭环）

| 批次 | 原计划 | 实际净缺口 |
|---|---|---|
| Batch 1 docs/test | ~20 笔 | **3 个证据文件** ✅ 已交付（475c246b2） |
| Batch 2 后端 | ~20 笔 | **4 个文件**（1e510cf56 内核：relrefs 解析器修复族） |
| Batch 3 前端 | ~30 笔 | **≈0**（96 已收编 / 57 main 更新 / 3 悬空） |
| Batch 4 迁移 | ~14 笔 | **≈0 独立可搬**（983j-r 整包与 Batch 2 后端强耦合） |

**分支对 main 的增量价值已在 `11dc125c4`（V2 admin 面）与 `9a0999515`（验收文档）两次收编中拿完。** 剩余独有内容全部锚在 main 未采纳的「视图+日物化」读侧方案上——该方案若要落地是**整分支合入的 owner 决策**（8 迁移 + 11 Go 文件 + web 组件 + TSV 重排 + fresh 验证），不是分批搬运。分步 cherry-pick 战役的合理终态：**Batch 1（已交付）+ Batch 2 的 4 文件摘录后，分支归档为「读侧加速候选方案」待 owner 立项**。

---

## 🚀 生产环境追平交付（2026-11-27 深夜轮）

### 211 追平：✅ 已完成

- **只读取证**（起点）：现役 `2.6.2-63d9c4f3-dirty-20261127-211` build_seq=3310（docker build 现场构建、非蓝绿链产物），基座 `63d9c4f3f` 是 main 祖先；PR #1/#2 当时均待合并 ⇒ 追平不依赖它们（净缺口是证据文件+测试面，无运行时行为）。
- **迁移面预核**：999b/c/d + 633（幂等补登）+ 1000/1001/1002（幂等重放：session_turns billing 4 列已在、billing_retry_queue 已在）+ 492（表已在）⇒ 全幂等；中途 tip 又前进（4aa482821 L138 配额制带 1003），1003 亦为 IF NOT EXISTS 全幂等范式。
- **部署过程中撞上的三件事**（都已定位/解决）：
  1. **main tip 半成品**：4aa482821 的 TSV 第 417 行登记了 `1004_three_tier_model_policies.sql` 但 canonical/embeddata 文件缺失 ⇒ test-gate 六红正确拦截（本地隔离树复现同红=100% 上游引入）。随后并行车（cursor 检出，同仓库体系）推了 1004 完整五点 + 42P16 修复（52ae9c32f2：`tenant_model_policies_active` 视图 ensure 同步 mode 列——211 库该视图含并行车本地加的 mode 列，main 的 7 列视图 CREATE OR REPLACE 必删列）到 codeup/main（7b938ced5）⇒ 最终追平锚定该尖端。
  2. **环境缺口**：211 部署库 SSOT（deploy-lib）断链 ⇒ 从本机 scp 到 `~/workspace/ai-native-tools/deploy/`；`~/.llm-gateway-211.env` 不存在 ⇒ 用 demoadmin（super_admin，/api/auth/token 实证）落凭据文件；test-gate 的 alpine 容器缺 bash/git ⇒ build `golang:1.27.4-alpine-bash`（DD_TEST_GO_IMAGE 注入）；引擎验收默认用户名 admin（密码已失效）⇒ 凭据文件补 `DD_ADMIN_USER=demoadmin`。
  3. **并行车道协作**：撞部署锁 3 次（目录型锁+metadata 含来源 pid）；并行车活锁等其自然释放；残留空锁目录 rmdir 后发车。并行车同期在 211 做了双活副节点（--start-secondary）实验，留下 s 槽容器重启循环（缺 DB/Redis env，它的未完成配置）。
- **最终交付判据（三绿）**：① `version.json` = `2.6.2-7b938ced-20261127-211` build_seq=3321 git_sha=7b938ced（**非 dirty**）；② readyz 200（database+redis connected）；③ 迁移面：通道日志 999b/c/d+1000/1001/1002/1003/1004 全部 `already applied`（installer receipts 幂等语义）+ 9 组对象级实证（organizations / org_model_policies / session_turns billing 4 列 / maas_bill_status_audit / maas_monthly_bills 支付列 / system_probe_runs.total_tokens / billing_retry_queue / tool_usage_stats / 973 投影 backfill_complete=t）。
- **灰度保全**：并行车留在 `deploy-seamless-docker.sh` 的 memora prewarm 灰度改动已备份 `/root/deploy-seamless-docker.sh.gray-backup-20261127` + git stash。
- **遗留观察**：① 211 库 schema_migrations 无 999b-d/1000-1004 登记行（通道按 installer receipts 判幂等跳过；fresh 重建若以此簿为准会重放，IF NOT EXISTS 无害）；② prompt_injection 检测日志 UTF8 断字节（22021）为存量数据面问题，非本次引入。

### 245/154 追平：未到期（按计划 12 月启动）

211 的部署配方与四道坎的解法已全部沉淀（见本节+memory），245/154 追平时直接复用。
