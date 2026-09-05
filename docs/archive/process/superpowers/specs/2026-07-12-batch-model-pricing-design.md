# 批量模型定价 + 复制粘贴修正 — 设计文档

日期: 2026-07-12

## 1. 问题与目标

### 现状
- **/model-pricing**（标准模型定价/积分）：只有逐行编辑弹窗，无多选、无批量操作、无复制粘贴
- **/pricing**（成本价格）：表格视图有复选框 + "粘贴到所选"，但 pasteToSelected 存在 Bug 不生效；缺乏按模型维度的批量定价入口

### 目标
1. **/model-pricing** 增加：多选 → 批量定价弹窗、行级复制、多选粘贴
2. **/pricing** 修正 pasteToSelected Bug，并增加「按模型批量定价」
3. 为 `/model-pricing` 新增后端批量 upsert API

---

## 2. 架构改动一览

```
Frontend                              Backend
─────────────────────────────────────────────────
StandardModelPricingView.vue  ───→  POST /api/admin/maas/model-rates/batch  (新增)
PricingManagementView.vue     ───→  POST /api/pricing/bulk-update           (已有, 修正)
```

---

## 3. 详细设计

### 3.1 /model-pricing 前端 (StandardModelPricingView.vue)

**改动范围：**
1. 表格加 checkbox 列 + 全选
2. 每行加「复制」按钮
3. 底部批量操作栏：「粘贴到所选」「批量定价」
4. 批量定价弹窗
5. 编辑弹窗内加「复制」「粘贴」

#### 3.1.1 数据结构
```ts
const selectedRows = ref<Set<number>>(new Set())       // 勾选的 canonical_id
const clipboard = ref<ClipboardData | null>(null)       // 复制缓冲区

interface ClipboardData {
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in: number
  credits_per_1m_cache_out: number
}

interface BatchPricingForm {
  credits_per_1m_in: number
  credits_per_1m_out: number
  credits_per_1m_cache_in: number
  credits_per_1m_cache_out: number
}
```

#### 3.1.2 交互流程

**复制：**
- 点击行「复制」按钮 → 将该行的 `credits_per_1m_*` 四个值存入 `clipboard`
- 提示"已复制"

**粘贴到所选：**
- 必须存在 `clipboard` 且 `selectedRows.size > 0`
- 对每个选中 `canonical_id`，调用 `POST /api/admin/maas/model-rates/batch`，将 clipboard 的四个值 + 对应 manual 标记（`manual_* = true`）批量写入
- 成功后刷新列表

**批量定价：**
- 点击「批量定价」→ 弹出 modal，填写 `in/out/cache_in/cache_out` 四个积分值
- 确认 → 对每个选中 `canonical_id` 调用 batch API，四个 manual 标记全设为 true
- 成功后刷新列表

**编辑弹窗复制粘贴：**
- 编辑弹窗 footer 加「复制价格」「粘贴」两个按钮
- 复制：将当前编辑表单值写入 clipboard
- 粘贴：将 clipboard 值填入编辑表单（保留手动勾选状态）

#### 3.1.3 UI 布局（表格区）

```
[☐]  标准模型  厂家  输入        输出        缓存读      缓存写   状态      操作
[☐]  gpt-4o   OpenAI  1,234   2,345    500      600     全手工  定价 复制
[☐]  claude-3  Anthropic  800  1,600    300      400     全手工  定价 复制
...
──────────────────────────────────────────────────────
已选 2 项  [粘贴到所选]  [批量定价]
```

### 3.2 /model-pricing 后端 (maas_handlers.go + maas/model_rates.go)

**新增 API：`POST /api/admin/maas/model-rates/batch`**

```go
// Request
{
  "updates": [
    {
      "canonical_id": 42,
      "credits_per_1m_in": 1000,
      "credits_per_1m_out": 2000,
      "credits_per_1m_cache_in": 500,
      "credits_per_1m_cache_out": 600,
      "manual_in": true,
      "manual_out": true,
      "manual_cache_in": true,
      "manual_cache_out": true
    }
  ]
}

// Response
{ "updated": 5 }
```

逻辑：循环调用 `UpsertModelRate`（复用现有方法），返回成功更新的数量。

### 3.3 /pricing 前端 (PricingManagementView.vue)

#### 3.3.1 修复 pasteToSelected

问题排查方向：
1. **clipboard 字段对齐**：检查 `pasteToSelected` 发送的 JSON 与后端 `pricingBulkUpdate` 的 struct tag 是否一致
2. **fetch import 的 Content-Type**：第 887 行 `importCsv` 中设置了 `'Content-Type': 'application/json'` 但实际发送的是 FormData（浏览器会自动设置 multipart boundary）。这是一个明显 bug，但和粘贴无关。
3. **selectedRows 分页丢失**：切换页面时 `selectedRows` 被清空（`Set` 是引用类型，但翻页后会重新 fetch，选中状态不会跨页保留），用户可能选了另一页的再切换回来，看不到选中项
4. **authHeaders 问题**：确认 token 未过期

修复措施：
- 确保 `pasteToSelected` 发送的字段中，`cache_read_price_per_1m` 和 `cache_write_price_per_1m` 使用正确的 key（下划线命名 vs camelCase）
- 修正 `importCsv` 的 Content-Type header
- 粘贴成功后显示更明确的反馈信息
- 修复后做端到端验证

#### 3.3.2 新增「按模型批量定价」

**交互流程：**
1. 表格视图上方加「按模型批量定价」按钮
2. 点击 → 弹窗第一步：模型选择器（现有 `ModelPicker` 组件）
3. 选择模型后 → 弹窗第二步：列出该模型下所有 offer（provider + credential + 当前价格）
4. 勾选要定价的 offer
5. 填写四个价格维度（in/out/cache_read/cache_write）+ 币种 + 计费模式
6. 确认 → `POST /api/pricing/bulk-update`

**不新增后端 API**，复用 `/api/pricing/bulk-update`。

**UI 实现方案（两种）：**

| 方案 | 描述 | 推荐 |
|------|------|------|
| A: 独立 Modal 两步流 | 弹窗 Step1 选模型 → Step2 列出 offer + 定价表单 | 推荐，用户流程清晰 |
| B: 表格内联展开 | 选中模型行后下方展开 offer 列表，勾选后定价 | 与现有树视图功能重叠 |

采用方案 A。

---

## 4. 改动文件清单

| 文件 | 改动类型 | 说明 |
|------|---------|------|
| `web/src/views/StandardModelPricingView.vue` | 修改 | 加 checkbox、复制、粘贴、批量定价弹窗 |
| `web/src/api/maas.ts` | 新增 | `batchUpsertAdminMaasModelRates()` API 函数 |
| `admin/maas_handlers.go` | 修改 | 注册 `/api/admin/maas/model-rates/batch` 路由 |
| `maas/model_rates.go` | 新增 | `BatchUpsertModelRates()` 方法 |
| `web/src/views/PricingManagementView.vue` | 修改 | 修复 pasteToSelected + 新增按模型批量定价 |

---

## 5. 边界情况

- 粘贴到所选时，clipboard 可能为空 → 禁用按钮
- 批量定价时所有 manual 标记全设为 true → 若值为空则提示
- 跨页选择不影响分页
- 定价后端 bulk-update 是多条独立 SQL，部分成功部分失败 → 返回实际更新数
