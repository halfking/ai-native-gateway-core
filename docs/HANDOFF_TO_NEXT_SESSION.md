# 会话交接文档 — i18n P0 迁移继续

> 交接时间：2026-07-05
> 上下文使用：89708 / 200000 tokens（45%）
> 当前状态：P0 迁移计划已完成，准备开始实施

---

## ✅ 已完成工作（本会话）

### 1. 首页与导航 100% i18n 化
- ✅ 8 种语言完整支持
- ✅ 30+ 导航项全部翻译
- ✅ 44 模块 locale 架构

### 2. 内容中性化
- ✅ 去除地域化表述
- ✅ 核心价值："内核开源"
- ✅ 图标去政治化：📖

### 3. 审计与规划
- ✅ 72 个视图覆盖率审计
- ✅ P0 10 个核心视图迁移计划
- ✅ 详细的 5 步迁移流程

### 4. 部署
- ✅ 3 次生产环境部署
- ✅ 版本：r1.13-done-41561148-20260705-50
- ✅ URL：https://__DOMAIN_1__

---

## 📋 下一步任务：P0 第 1 批迁移（4 小时）

### 任务 1：RequestLogsView.vue（1.5h）
**文件位置**：`web/src/views/RequestLogsView.vue`
**locale 文件**：`web/src/locales/zh-CN/requests.ts`（368 行，已存在）
**当前状态**：
- 已有 `useI18n` 引入（第 4 行）
- 硬编码中文：1878 字符
- 已有部分 i18n 基础（3 处使用）

**迁移步骤**：
1. 找出所有硬编码中文字符串
2. 映射到 `requests.ts` 的对应键
3. 替换为 `t('requests.xxx.yyy')`
4. 测试构建

### 任务 2：ModelsView.vue（2h）
**文件位置**：`web/src/views/ModelsView.vue`
**locale 文件**：`web/src/locales/zh-CN/models.ts`（已存在）
**当前状态**：完全硬编码，需要从头开始

### 任务 3：CredentialsView.vue（0.5h）
**文件位置**：`web/src/views/CredentialsView.vue`
**locale 文件**：`web/src/locales/zh-CN/credentialMonitor.ts`（已存在）
**当前状态**：完全硬编码

---

## 🔧 标准迁移流程（每个视图）

### 步骤 1：审计（5 分钟）
```bash
python3 << 'PYTHON'
import re
with open('web/src/views/RequestLogsView.vue', 'r') as f:
    content = f.read()
chinese = re.findall(r'[\u4e00-\u9fa5]', content)
print(f'硬编码字符数：{len(chinese)}')
PYTHON
```

### 步骤 2：添加 useI18n（如果没有）
```vue
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
const { t } = useI18n()
</script>
```

### 步骤 3：替换硬编码
```vue
<!-- 之前 -->
<h1>请求日志</h1>

<!-- 之后 -->
<h1>{{ t('requests.list.title') }}</h1>
```

### 步骤 4：处理动态文案
```vue
<!-- 带变量 -->
{{ t('requests.list.cacheDesc', { total: 100 }) }}
```

### 步骤 5：验证
```bash
npm run build
git diff web/src/views/RequestLogsView.vue
```

---

## 📂 关键文件位置

### 视图文件
```
web/src/views/RequestLogsView.vue
web/src/views/ModelsView.vue
web/src/views/CredentialsView.vue
web/src/views/ProvidersView.vue
web/src/views/RoutingOverviewView.vue
web/src/views/DecisionsView.vue
web/src/views/TenantsView.vue
web/src/views/TenantDashboardView.vue
web/src/views/UsersView.vue
web/src/views/SessionListView.vue
```

### Locale 文件（8 种语言）
```
web/src/locales/zh-CN/requests.ts（368 行）
web/src/locales/zh-CN/models.ts
web/src/locales/zh-CN/credentialMonitor.ts
web/src/locales/zh-CN/providers.ts
web/src/locales/zh-CN/routing.ts
web/src/locales/zh-CN/decisions.ts
web/src/locales/zh-CN/tenants.ts
web/src/locales/zh-CN/dashboard.ts
web/src/locales/zh-CN/users.ts
web/src/locales/zh-CN/sessions.ts
```

### 文档
```
I18N_COVERAGE_REPORT.md — 覆盖率审计
P0_MIGRATION_PLAN.md — 详细迁移计划
I18N_OPTIMIZATION_PROPOSAL.md — 整体方案
```

---

## ⚠️ 注意事项

### 1. locale 键已存在
44 模块 × 8 语言的 locale 文件**已经就位**，迁移主要是修改 .vue 文件。

### 2. 保持构建通过
每完成一个视图，立即运行 `npm run build` 验证。

### 3. 处理动态内容
- 状态映射：使用 `computed(() => t(...))`
- 表格列标题：提取为 computed
- 枚举值：computed + t()

### 4. 验证方式
```bash
# 构建
npm run build

# 检查剩余硬编码
python3 -c "
import re
with open('web/src/views/RequestLogsView.vue', 'r') as f:
    content = f.read()
chinese = re.findall(r'[\u4e00-\u9fa5]', content)
print(f'剩余硬编码：{len(chinese)}')
"
```

---

## 📊 当前进度

| 任务 | 状态 | 优先级 |
|---|---|---|
| 首页 + 导航 | ✅ 完成 | P0 |
| 内容中性化 | ✅ 完成 | P0 |
| 覆盖率审计 | ✅ 完成 | P0 |
| P0 迁移计划 | ✅ 完成 | P0 |
| **RequestLogsView** | ⏸️ 待开始 | P0 |
| **ModelsView** | ⏸️ 待开始 | P0 |
| **CredentialsView** | ⏸️ 待开始 | P0 |
| 其余 7 个 P0 | ⏸️ 待开始 | P0 |

---

## 🚀 建议的工作流程

### 新会话开始后

1. **回顾上下文**（2 分钟）
   ```bash
   cd ~/workspace/official-deploy/services/llm-gateway-go
   cat HANDOFF_TO_NEXT_SESSION.md
   cat P0_MIGRATION_PLAN.md
   ```

2. **开始第 1 个视图**（1.5 小时）
   - 读取 `RequestLogsView.vue`
   - 读取 `requests.ts` 查看可用的键
   - 逐步替换硬编码
   - 验证构建

3. **开始第 2 个视图**（2 小时）
   - ModelsView.vue
   - 复用第 1 个视图的模式

4. **开始第 3 个视图**（0.5 小时）
   - CredentialsView.vue
   - 模式已熟练，快速完成

5. **部署验证**（0.5 小时）
   - 构建
   - 部署到 184
   - 浏览器实测

---

## 💡 快速启动命令

```bash
# 切换到项目目录
cd ~/workspace/official-deploy/services/llm-gateway-go

# 检查当前状态
git status
git log --oneline -5

# 开始工作
git stash pop  # 如果有暂存的改动

# 审计第一个视图
python3 << 'PYTHON'
import re
with open('web/src/views/RequestLogsView.vue', 'r') as f:
    content = f.read()
chinese = re.findall(r'[\u4e00-\u9fa5]', content)
print(f'RequestLogsView 硬编码字符数：{len(chinese)}')
PYTHON
```

---

## 📞 联系上下文

### Git 仓库
```
远程：__REPO_URL_1__.git
当前分支：main
最新提交：f4167a35 docs(i18n): P0 核心视图迁移计划
```

### 部署环境
```
生产：https://__DOMAIN_1__ (__PUB_IP_1__)
版本：r1.13-done-41561148-20260705-50
状态：✅ Running
```

---

**交接完成。建议在新会话中继续，有充足的上下文空间进行 P0 迁移。**
