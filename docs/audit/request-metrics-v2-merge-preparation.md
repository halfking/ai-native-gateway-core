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
